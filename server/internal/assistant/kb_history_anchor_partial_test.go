package assistant

import (
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A year whose search stopped before its last page is not covered, whatever its first page found: a later page
// refused, or one saying there is more without a new cursor. Its part of the chat is read backwards through the
// filter, so the owner's messages only a later page would have listed are kept too.
func TestHistoryPartiallySearchedYearFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		next func(q connectorwire.HistoryAnchorsQuery, first connectorwire.HistoryAnchor) (connectorwire.HistoryAnchors, error)
	}{
		{"a later page refused", func(q connectorwire.HistoryAnchorsQuery, first connectorwire.HistoryAnchor) (connectorwire.HistoryAnchors, error) {
			if q.Cursor == "" {
				return connectorwire.HistoryAnchors{Anchors: []connectorwire.HistoryAnchor{first}, More: true, Next: "page2"}, nil
			}
			return connectorwire.HistoryAnchors{}, &rpc.Error{Code: "permission_denied"}
		}},
		{"more without a new cursor", func(q connectorwire.HistoryAnchorsQuery, first connectorwire.HistoryAnchor) (connectorwire.HistoryAnchors, error) {
			return connectorwire.HistoryAnchors{Anchors: []connectorwire.HistoryAnchor{first}, More: true}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
			y := now.Year()
			start := time.Date(y-2, 6, 1, 8, 0, 0, 0, time.UTC)
			step := 12 * time.Hour
			n := int(now.Sub(start)/step) - 2
			msgs := historyConv("g", n, start, step)
			prevStart := time.Date(y-1, 1, 1, 0, 0, 0, 0, time.UTC)
			thisStart := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
			firstPrev := int(prevStart.Sub(start)/step) + 1
			lastPrev := int(thisStart.Sub(start)/step) - 1
			// Last year has two of the owner's messages; the search's first page lists only the newer one.
			listed, missed := lastPrev-100, firstPrev+200
			for _, p := range []int{listed, missed, n - 100} {
				msgs[p].Mine = true
			}
			src := positionalSource("g", msgs, listed, n-100)
			first := src.anchors[y-1][0]
			src.anchorHook = func(q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error, bool) {
				if q.Since.Year() != y-1 {
					return connectorwire.HistoryAnchors{}, nil, false
				}
				a, err := tc.next(q, first)
				return a, err, true
			}
			ht := newHistoryTest(t, src)
			ht.clock.t = now
			ht.plan(true, nil)
			ht.run()
			cache, err := loadHistoryAnchors(ht.data, testSource)
			if err != nil {
				t.Fatal(err)
			}
			if got := cache.uncovered(); !slices.Contains(got, y-1) {
				t.Fatalf("last year counted as covered: uncovered %v, years %+v", got, cache.Years)
			}
			kept := ht.kept("g")
			for _, p := range []int{listed, missed, n - 100} {
				if !slices.Contains(kept, p) {
					t.Fatalf("the owner's message at %d is missing (listed %d, missed %d): kept %v", p, listed, missed, kept)
				}
			}
			if st := ht.state("g"); st.State != kb.HistoryDone || len(st.Positions.Fallback) != 0 || st.Positions.Year != 0 {
				t.Fatalf("state %+v %+v", st, st.Positions)
			}
		})
	}
}
