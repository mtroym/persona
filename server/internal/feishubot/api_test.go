package feishubot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// retryAPIClient serves a token endpoint and h for the rest, counts hits/token fetches, and no-ops backoff. No t.Parallel in this package: callSleep is package-level.
func retryAPIClient(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n *atomic.Int32)) (*Client, *httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var hits, tokens atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			tokens.Add(1)
			okJSON(w, map[string]any{"code": 0, "tenant_access_token": "tok", "expire": 7200})
			return
		}
		hits.Add(1)
		h(w, r, &hits)
	}))
	t.Cleanup(srv.Close)
	c := New("cli_x", "secret", srv.URL)
	prev := callSleep
	callSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { callSleep = prev })
	return c, srv, &hits, &tokens
}

func okJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
func botInfoOK(w http.ResponseWriter) {
	okJSON(w, map[string]any{"code": 0, "bot": map[string]any{"open_id": "o1"}})
}
func callOnce(c *Client) error { _, err := c.BotInfo(context.Background()); return err }

// Transient answers are retried until the third attempt; waits/tokens give the expected backoff waits and token fetches (a token-gone refresh spends no slot, and must not refresh again for later blips).
func TestCallRetriesTransientAnswers(t *testing.T) {
	cases := []struct {
		name   string
		fail   func(w http.ResponseWriter, n *atomic.Int32)
		waits  int // backoff waits before the success
		tokens int // token endpoint fetches
	}{
		{"503 html page", func(w http.ResponseWriter, _ *atomic.Int32) {
			http.Error(w, "bad gateway", http.StatusServiceUnavailable)
		}, 2, 1},
		{"http 429", func(w http.ResponseWriter, _ *atomic.Int32) {
			w.WriteHeader(http.StatusTooManyRequests)
			okJSON(w, map[string]any{"code": 0})
		}, 2, 1},
		{"rate limit code", func(w http.ResponseWriter, _ *atomic.Int32) {
			okJSON(w, map[string]any{"code": codeRateLimited})
		}, 2, 1},
		{"5xx json body", func(w http.ResponseWriter, _ *atomic.Int32) {
			w.WriteHeader(http.StatusBadGateway)
			okJSON(w, map[string]any{"code": 12345})
		}, 2, 1},
		{"token expired then 503", func(w http.ResponseWriter, n *atomic.Int32) {
			if n.Load() == 1 {
				okJSON(w, map[string]any{"code": 99991663})
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var slept []time.Duration
			c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, n *atomic.Int32) {
				if n.Load() < 3 {
					tc.fail(w, n)
				} else {
					botInfoOK(w)
				}
			})
			callSleep = func(_ context.Context, d time.Duration) error {
				if i := len(slept); i < len(callBackoff) && (d < callBackoff[i]*3/4 || d > callBackoff[i]*5/4) {
					t.Errorf("wait %d = %v, want +/-25%% of %v", i, d, callBackoff[i])
				}
				slept = append(slept, d)
				return nil
			}
			info, err := c.BotInfo(context.Background())
			if err != nil || info.OpenID != "o1" || hits.Load() != 3 || len(slept) != tc.waits || tokens.Load() != int32(tc.tokens) {
				t.Fatalf("info=%+v hits=%d waits=%d tokens=%d err=%v",
					info, hits.Load(), len(slept), tokens.Load(), err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type errBody struct{ error }

func (b errBody) Read([]byte) (int, error) { return 0, b.error }
func (b errBody) Close() error             { return nil }

// A failing connection and a body read that fails both use all three attempts and return the last error.
func TestCallTransportFailuresBounded(t *testing.T) {
	fails := map[string]func() (*http.Response, error){
		"do error": func() (*http.Response, error) { return nil, errors.New("connection reset by peer") },
		"body read error": func() (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: errBody{errors.New("unexpected EOF")}}, nil
		},
	}
	for name, fail := range fails {
		t.Run(name, func(t *testing.T) {
			c, srv, hits, _ := retryAPIClient(t, func(http.ResponseWriter, *http.Request, *atomic.Int32) {})
			c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					return srv.Client().Transport.RoundTrip(req)
				}
				hits.Add(1)
				return fail()
			})}
			err := callOnce(c)
			if err == nil || hits.Load() != callMaxAttempts ||
				!strings.Contains(err.Error(), "reset") && !strings.Contains(err.Error(), "EOF") {
				t.Fatalf("err=%v hits=%d", err, hits.Load())
			}
		})
	}
}

// A cancelled context returns at once: before the request goes out, and while the backoff waits.
func TestCallCancelledContextDoesNotRetry(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		c, _, hits, _ := retryAPIClient(t, func(http.ResponseWriter, *http.Request, *atomic.Int32) {})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.BotInfo(ctx); !errors.Is(err, context.Canceled) || hits.Load() != 0 {
			t.Fatalf("err=%v hits=%d", err, hits.Load())
		}
	})
	t.Run("during backoff", func(t *testing.T) {
		c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		ctx, cancel := context.WithCancel(context.Background())
		callSleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
		if _, err := c.BotInfo(ctx); !errors.Is(err, context.Canceled) || hits.Load() != 1 {
			t.Fatalf("err=%v hits=%d", err, hits.Load())
		}
	})
}

// A 4xx business error is not a transport problem: one attempt, APIError with its code.
func TestCallBusinessErrorNotRetried(t *testing.T) {
	c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
		w.WriteHeader(http.StatusBadRequest)
		okJSON(w, map[string]any{"code": 230002})
	})
	var apiErr *APIError
	if err := callOnce(c); !errors.As(err, &apiErr) || apiErr.Code != 230002 || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

// Token fetch failures use the bounded backoff (a uuid-less POST retries pre-send); after tokenGone a failed forced fetch keeps forcing, so the POST lands once on the new token.
func TestCallTokenFetchRetries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		post        bool
		plainFails  int
		forcedFails int32
		fetches     int32
		hits        int32
		landed      int32
		sleeps      int32
	}{
		{"get recovers after a fetch failure", false, 1, 0, 2, 1, 1, 1},
		{"uuid-less post retries before its body lands", true, 1, 0, 2, 1, 1, 1},
		{"all fetches fail returns the token error", false, 3, 0, 3, 0, 0, 2},
		{"get retries the forced fetch with the new token", false, 0, 1, 3, 2, 1, 1},
		{"uuid-less post lands once with the new token", true, 0, 1, 3, 2, 1, 1},
		{"uuid-less post forced fetches all fail", true, 0, 2, 3, 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n, forcedN, hits, landed, slept, goneN atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					fail := goneN.Load() > 0 && forcedN.Add(1) <= tc.forcedFails || goneN.Load() == 0 && n.Add(1) <= int32(tc.plainFails)
					if fail {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					okJSON(w, map[string]any{"code": 0, "tenant_access_token": map[bool]string{true: "tok2", false: "tok1"}[goneN.Load() > 0], "expire": 7200})
					return
				}
				hits.Add(1)
				if tc.plainFails == 0 && r.Header.Get("Authorization") != "Bearer tok2" {
					goneN.Store(1) // rejected at the auth layer; the body never lands
					okJSON(w, map[string]any{"code": 99991663})
					return
				}
				landed.Add(1)
				okJSON(w, map[string]any{"code": 0, "data": map[string]any{"message_id": "m1"}, "bot": map[string]any{"open_id": "o1"}})
			}))
			defer srv.Close()
			c := New("cli_x", "secret", srv.URL)
			prev := callSleep
			callSleep = func(context.Context, time.Duration) error { slept.Add(1); return nil }
			defer func() { callSleep = prev }()
			var err error
			if tc.post {
				_, err = c.Send(context.Background(), "chat_id", "c1", TypeText, TextContent("hi"), "")
			} else {
				_, err = c.BotInfo(context.Background())
			}
			wantErr := tc.landed == 0
			if (err != nil) != wantErr || n.Load()+forcedN.Load() != tc.fetches || hits.Load() != tc.hits ||
				landed.Load() != tc.landed || slept.Load() != tc.sleeps || wantErr && !strings.Contains(err.Error(), "token") {
				t.Fatalf("err=%v fetches=%d hits=%d landed=%d sleeps=%d", err, n.Load()+forcedN.Load(), hits.Load(), landed.Load(), slept.Load())
			}
		})
	}
}

// 429 honors a Retry-After delta/date on the HTTP status or body rate-limit code; past the cap it gives up at once.
func TestCallRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		after   string // empty sends an HTTP date 10s out
		status  int
		code    int
		hits    int32
		minWait time.Duration
	}{
		{"delta seconds on 429", "5", http.StatusTooManyRequests, 0, 3, 5 * time.Second},
		{"delta seconds on body code", "2", http.StatusOK, codeRateLimited, 3, 2 * time.Second},
		{"http date on 429", "", http.StatusTooManyRequests, 0, 3, 8 * time.Second},
		{"retry-after exactly at the cap", strconv.Itoa(int(callMaxRetryWait / time.Second)), http.StatusTooManyRequests, 0, 3, callMaxRetryWait},
		{"past the cap gives up", strconv.Itoa(int(callMaxRetryWait/time.Second) + 30), http.StatusTooManyRequests, codeRateLimited, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var slept []time.Duration
			after := tc.after
			if after == "" {
				after = time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
			}
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
				if n.Load() < 3 {
					w.Header().Set("Retry-After", after)
					w.WriteHeader(tc.status)
					okJSON(w, map[string]any{"code": tc.code})
					return
				}
				botInfoOK(w)
			})
			callSleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
			_, err := c.BotInfo(context.Background())
			wantErr := tc.hits == 1
			if (err != nil) != wantErr || hits.Load() != tc.hits || len(slept) != int(tc.hits)-1 ||
				wantErr && errorCode(err) != codeRateLimited {
				t.Fatalf("err=%v wantErr=%v hits=%d sleeps=%v", err, wantErr, hits.Load(), slept)
			}
			for _, d := range slept {
				if d < tc.minWait || tc.after != "" && d != tc.minWait {
					t.Fatalf("sleep %v, want %v", d, tc.minWait)
				}
			}
		})
	}
}

// A POST is retried only when it carries a uuid, and every attempt keeps the same uuid.
func TestSendUUIDRetryPolicy(t *testing.T) {
	cases := []struct{ name, uuid string }{{"without uuid", ""}, {"with uuid", "send:c1:answer"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, n *atomic.Int32) {
				var in map[string]any
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &in)
				if u, _ := in["uuid"].(string); u != tc.uuid {
					t.Errorf("uuid=%q, want %q", u, tc.uuid)
				}
				switch {
				case tc.uuid == "": // uuid-less POST: one rate-limit answer, no retry
					okJSON(w, map[string]any{"code": codeRateLimited})
				case n.Load() == 1:
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					okJSON(w, map[string]any{"code": 0, "data": map[string]any{"message_id": "m1"}})
				}
			})
			id, err := c.Send(context.Background(), "chat_id", "c1", TypeText, TextContent("hi"), tc.uuid)
			if tc.uuid == "" {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != codeRateLimited || hits.Load() != 1 {
					t.Fatalf("err=%v hits=%d", err, hits.Load())
				}
				return
			}
			if err != nil || id != "m1" || hits.Load() != 2 {
				t.Fatalf("id=%q err=%v hits=%d", id, err, hits.Load())
			}
		})
	}
}

// A file whose length is said up front to be over the limit is refused before its body is read.
func TestResourceTooLargeIsRefusedUnread(t *testing.T) {
	var wrote atomic.Int64
	c, _, _, _ := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, _ *atomic.Int32) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 4; i++ {
			n, err := w.Write(make([]byte, 1024))
			wrote.Add(int64(n))
			if err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	_, _, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err %v", err)
	}
}

// A JSON file the user sent comes back from a 200 download labeled application/json: it is the file, not an error,
// even when it looks like an API error answer (a saved error response, say), and it is not downloaded twice.
func TestResourceJSONFileIsTheFile(t *testing.T) {
	for _, body := range []string{
		`{"name":"settings","debug":true}`,
		`[{"code":7,"msg":"x"}]`,
		`{"code":7,"name":"no msg"}`,
		`{"code":"E1","msg":"a string code"}`,
		`{"code":0,"msg":"ok"}`,
		`{"code":40001,"msg":"invalid token"}`,
		`{"code":99991663,"msg":"token expired"}`,
	} {
		t.Run(body, func(t *testing.T) {
			c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			})
			b, ct, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024)
			if err != nil || string(b) != body || ct != "application/json" || hits.Load() != 1 || tokens.Load() != 1 {
				t.Fatalf("b=%q ct=%q err=%v hits=%d tokens=%d", b, ct, err, hits.Load(), tokens.Load())
			}
		})
	}
}

// A JSON file over the limit is refused as too large, like any other file.
func TestResourceJSONFileTooLarge(t *testing.T) {
	c, _, _, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"pad":"`+strings.Repeat("x", 2048)+`"}`)
	})
	if _, _, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err %v", err)
	}
}

// A token-gone answer (non-200) is retried once with a fresh token; a second one is the error.
func TestResourceTokenGoneIsRetriedOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gone  int32 // how many answers are token-gone before the file
		isErr bool
	}{{"fresh token fixes it", 1, false}, {"still gone after one retry", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
				if n.Load() <= tc.gone {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					okJSON(w, map[string]any{"code": 99991663, "msg": "token expired"})
					return
				}
				_, _ = io.WriteString(w, "file bytes")
			})
			b, _, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024)
			if hits.Load() != 2 || tokens.Load() != 2 {
				t.Fatalf("hits=%d tokens=%d", hits.Load(), tokens.Load())
			}
			if tc.isErr {
				if errorCode(err) != 99991663 || b != nil {
					t.Fatalf("b=%q err=%v", b, err)
				}
				return
			}
			if err != nil || string(b) != "file bytes" {
				t.Fatalf("b=%q err=%v", b, err)
			}
		})
	}
}
