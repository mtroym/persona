package mail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// Parts go-message cannot fully decode: an unknown charset (the part comes back with its raw body) or an unknown
// transfer encoding (no part comes back). Neither may hide the text, snippet or attachments after it.

const (
	unknownPDF = "--outer\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=report.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nYWJj\r\n"
	unknownUUText = "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: x-uuencode\r\n\r\n" +
		"begin 644 note.txt\r\n$8F]D>0``\r\n`\r\nend\r\n"
)

type unknownMIMEAttachment struct {
	index          int
	filename, mime string
	data           string
}

type unknownMIMECase struct {
	name        string
	parts       string // the multipart/mixed body with boundary "outer", closing boundary included
	text, html  string
	snippet     string
	attachments []unknownMIMEAttachment
}

func unknownMIMECases() []unknownMIMECase {
	pdf := unknownMIMEAttachment{1, "report.pdf", "application/pdf", "abc"}
	var cases []unknownMIMECase
	// None of these is a charset go-message/charset knows.
	for _, cs := range []string{"unknown-8bit", "x-unknown", "ansi", "default", "utf-7"} {
		cases = append(cases, unknownMIMECase{
			name: "charset " + cs + " then attachment",
			parts: "--outer\r\nContent-Type: text/plain; charset=" + cs + "\r\nContent-Transfer-Encoding: 8bit\r\n\r\nraw body\r\n" +
				unknownPDF + "--outer--\r\n",
			text: "raw body", snippet: "raw body", attachments: []unknownMIMEAttachment{pdf},
		})
	}
	return append(cases,
		unknownMIMECase{
			name:  "x-uuencode then text then attachment",
			parts: "--outer\r\n" + unknownUUText + "--outer\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n" + unknownPDF + "--outer--\r\n",
			text:  "body", snippet: "body", attachments: []unknownMIMEAttachment{pdf},
		},
		unknownMIMECase{
			// No text/plain: Text falls back to the HTML as text.
			name: "nested alternative with the bad part first",
			parts: "--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n" +
				"--inner\r\n" + unknownUUText +
				"--inner\r\nContent-Type: text/html; charset=unknown-8bit\r\n\r\n<p>body</p>\r\n--inner--\r\n" +
				unknownPDF + "--outer--\r\n",
			text: "body ", html: "<p>body</p>", snippet: "body", attachments: []unknownMIMEAttachment{pdf},
		},
		unknownMIMECase{
			// The undecodable attachment has no header to count, so neither the list nor the download counts it.
			name: "attachments in an unknown charset and an unknown transfer encoding",
			parts: "--outer\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n" +
				"--outer\r\nContent-Type: text/csv; charset=unknown-8bit\r\nContent-Disposition: attachment; filename=table.csv\r\n\r\na,b\r\n" +
				"--outer\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=old.bin\r\n" +
				"Content-Transfer-Encoding: x-uuencode\r\n\r\nbegin 644 old.bin\r\n#86)C\r\n`\r\nend\r\n" +
				unknownPDF + "--outer--\r\n",
			text: "body", snippet: "body",
			attachments: []unknownMIMEAttachment{{1, "table.csv", "text/csv", "a,b"}, {2, "report.pdf", "application/pdf", "abc"}},
		},
	)
}

func unknownMIMERaw(c unknownMIMECase) string {
	return "From: Sender <sender@example.com>\r\nTo: recipient@example.com\r\nSubject: Unknown MIME\r\n" +
		"Message-ID: <unknown@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n" + c.parts
}

func checkUnknownMIME(t *testing.T, c unknownMIMECase, msg *MailMessage) {
	t.Helper()
	if msg.Text != c.text || msg.HTML != c.html {
		t.Fatalf("text = %q, html = %q, want %q, %q", msg.Text, msg.HTML, c.text, c.html)
	}
	if len(msg.Attachments) != len(c.attachments) {
		t.Fatalf("attachments = %+v, want %d", msg.Attachments, len(c.attachments))
	}
	for i, want := range c.attachments {
		a := msg.Attachments[i]
		if !strings.HasPrefix(a.ID, strconv.Itoa(want.index)+"-") || a.Filename != want.filename || a.Mime != want.mime ||
			a.Size != int64(len(want.data)) {
			t.Errorf("attachment %d = %+v, want %+v", i, a, want)
		}
	}
}

func TestParseMessageUnknownCharsetOrEncoding(t *testing.T) {
	for _, c := range unknownMIMECases() {
		t.Run(c.name, func(t *testing.T) {
			raw := unknownMIMERaw(c)
			msg := &MailMessage{}
			parseMessage([]byte(raw), msg)
			checkUnknownMIME(t, c, msg)
			if got := strings.Join(strings.Fields(textFromRaw([]byte(raw))), " "); got != c.snippet {
				t.Fatalf("textFromRaw = %q, want %q", got, c.snippet)
			}
		})
	}
}

// The list, mail_read and both download paths agree on the message once it went through a real IMAP server.
func TestUnknownCharsetOrEncodingListReadAndDownload(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	service := newServiceWithAccount(t, srv)
	ctx := context.Background()
	for _, c := range unknownMIMECases() {
		t.Run(c.name, func(t *testing.T) {
			uid := uint32(srv.appendMessage(t, "INBOX", unknownMIMERaw(c)))
			list, err := service.ListMessages(ctx, ListOptions{AccountID: "acc_test", Limit: 1})
			if err != nil || len(list.Messages) != 1 || list.Messages[0].UID != uid {
				t.Fatalf("list = %+v, %v", list, err)
			}
			if got := list.Messages[0].Snippet; got != c.snippet {
				t.Fatalf("list snippet = %q, want %q", got, c.snippet)
			}
			msg, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
			if err != nil {
				t.Fatal(err)
			}
			checkUnknownMIME(t, c, msg)
			if msg.Snippet != c.snippet || !msg.HasAttach {
				t.Fatalf("summary = %+v", msg.MailSummary)
			}
			path := "/api/v1/mail/messages/acc_test/" + itoa(uid) + "/attachments/"
			for i, a := range msg.Attachments {
				response := httptest.NewRecorder()
				service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+a.ID, nil))
				if response.Code != http.StatusOK || response.Body.String() != c.attachments[i].data ||
					response.Header().Get("ETag") != `"`+a.ID+`"` {
					t.Fatalf("download %s: %d %q %v", a.ID, response.Code, response.Body.String(), response.Header())
				}
				metadata, data, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", a.ID, MaxSavedAttachment)
				if err != nil || string(data) != c.attachments[i].data || metadata != a {
					t.Fatalf("save handoff %s: %+v %q %v", a.ID, metadata, data, err)
				}
			}
		})
	}
}

// Any other error still ends the walk, as the download returns it: only the two decode errors go-message reports as
// recoverable are passed over, so nothing after a malformed part is listed.
func TestParseMessageStopsAtOtherErrors(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\n" + unknownUUText +
		"--outer\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--outer\r\nthis header line has no colon\r\n\r\nlost\r\n" + unknownPDF + "--outer--\r\n"
	msg := &MailMessage{}
	parseMessage([]byte(raw), msg)
	if msg.Text != "body" || len(msg.Attachments) != 0 {
		t.Fatalf("text = %q, attachments = %+v, want the text before the malformed part and nothing after it", msg.Text, msg.Attachments)
	}
}
