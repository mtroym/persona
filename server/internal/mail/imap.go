package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	"github.com/nwverse/persona/server/internal/lazyre"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
	snippetRunes     = 160
	// snippetBytes is how much of the message body a list fetch asks for.
	// It is a BODY.PEEK partial section, so the rest never leaves the server.
	snippetBytes = 2048
)

// localScan bounds one page of a local header search (searchLocally).
type localScan struct {
	batch  int // envelopes per FETCH: small, since one batch of old mail can take seconds (NetEase 163)
	budget int // messages looked at per page
	// time is how long a page may search, counted from the start of its IMAP work (SELECT and the server's own
	// search included), so the matches can still be fetched inside networkTimeout.
	time time.Duration
}

var defaultLocalScan = localScan{batch: 100, budget: 2000, time: 6 * time.Second}

// pageFetch bounds the summary fetch of one page (listOne).
type pageFetch struct {
	batch   int           // summaries per FETCH
	reserve time.Duration // no further batch once the operation has less than this left
}

var defaultPageFetch = pageFetch{batch: 50, reserve: 6 * time.Second}

// pageFetchLimits is s.fetch with the defaults filled in (a zero Service has none).
func (s *Service) pageFetchLimits() pageFetch {
	l := s.fetch
	if l.batch <= 0 {
		l.batch = defaultPageFetch.batch
	}
	if l.reserve <= 0 {
		l.reserve = defaultPageFetch.reserve
	}
	return l
}

// accountLock serializes IMAP use of one account: connections are stateful
// (SELECT), so commands must not interleave. Unlike a sync.Mutex it is taken
// with a ctx: a caller whose ctx ends while it waits leaves the queue with
// ctx.Err() instead of taking its turn only to find out. Before (#2344) such
// a caller held everyone up for nothing, and on its turn dropped the cached
// connection on the way out, so the next live caller had to reconnect.
type accountLock chan struct{}

// acquire takes the lock, or returns ctx's error once ctx ends first. A ctx
// that is already over never takes the lock, even a free one.
func (l accountLock) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release gives the lock back; the caller must hold it.
func (l accountLock) release() { <-l }

// accountLock returns the lock serializing IMAP use of one account.
func (s *Service) accountLock(id string) accountLock {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opMu == nil {
		s.opMu = make(map[string]accountLock)
	}
	l, ok := s.opMu[id]
	if !ok {
		l = make(accountLock, 1)
		s.opMu[id] = l
	}
	return l
}

// Mailboxes lists the mailboxes of an account with their role and counters.
func (s *Service) Mailboxes(ctx context.Context, id string) ([]Mailbox, error) {
	a, err := s.account(id)
	if err != nil {
		return nil, err
	}
	statusOpts := &imap.StatusOptions{NumMessages: true, NumUnseen: true}
	var out []Mailbox
	err = s.withConn(ctx, a, func(c *imapclient.Client) error {
		out = nil
		data, err := c.List("", "*", listOptions(c.Caps(), statusOpts)).Collect()
		if err != nil {
			return err
		}
		needStatus := make([]*imap.ListData, 0, len(data))
		for _, ld := range data {
			if hasAttr(ld.Attrs, imap.MailboxAttrNoSelect) || hasAttr(ld.Attrs, imap.MailboxAttrNonExistent) {
				continue
			}
			mb := Mailbox{Name: ld.Mailbox, Role: roleOf(ld.Mailbox, ld.Attrs)}
			if ld.Status != nil {
				if ld.Status.NumMessages != nil {
					mb.Total = int(*ld.Status.NumMessages)
				}
				if ld.Status.NumUnseen != nil {
					mb.Unseen = int(*ld.Status.NumUnseen)
				}
				out = append(out, mb)
			} else {
				needStatus = append(needStatus, ld)
				out = append(out, mb)
			}
		}
		// Servers without LIST-STATUS: fall back to one STATUS per mailbox.
		for _, ld := range needStatus {
			st, err := c.Status(ld.Mailbox, statusOpts).Wait()
			if err != nil {
				continue
			}
			for j := range out {
				if out[j].Name == ld.Mailbox {
					if st.NumMessages != nil {
						out[j].Total = int(*st.NumMessages)
					}
					if st.NumUnseen != nil {
						out[j].Unseen = int(*st.NumUnseen)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// listOptions asks LIST only for the return options the server can take. RETURN
// needs LIST-EXTENDED (implied by IMAP4rev2): QQ Mail advertises none of these
// and never answers a LIST that carries RETURN, so such servers get a plain LIST
// and roles come from mailbox names, counters from one STATUS per mailbox.
// SPECIAL-USE is still asked of IMAP4rev2 servers, as it always was. status may
// be nil.
func listOptions(caps imap.CapSet, status *imap.StatusOptions) *imap.ListOptions {
	opts := &imap.ListOptions{}
	if !caps.Has(imap.CapListExtended) {
		return opts
	}
	opts.ReturnSpecialUse = caps.Has(imap.CapSpecialUse) || caps.Has(imap.CapIMAP4rev2)
	if status != nil && caps.Has(imap.CapListStatus) {
		opts.ReturnStatus = status
	}
	return opts
}

// roleOf maps a mailbox name and its SPECIAL-USE attributes to a role.
func roleOf(name string, attrs []imap.MailboxAttr) string {
	if strings.EqualFold(name, "INBOX") {
		return RoleInbox
	}
	for _, attr := range attrs {
		switch attr {
		case imap.MailboxAttrSent:
			return RoleSent
		case imap.MailboxAttrDrafts:
			return RoleDrafts
		case imap.MailboxAttrTrash:
			return RoleTrash
		case imap.MailboxAttrArchive:
			return RoleArchive
		case imap.MailboxAttrJunk:
			return RoleJunk
		case imap.MailboxAttrAll:
			// Gmail's "All Mail": not one of the roles the protocol defines.
			continue
		}
	}
	// Providers that do not set SPECIAL-USE: guess from the name.
	switch strings.ToLower(name) {
	case "sent", "sent items", "sent messages", "sent mail":
		return RoleSent
	case "drafts", "draft":
		return RoleDrafts
	case "trash", "deleted items", "deleted messages", "bin":
		return RoleTrash
	case "archive", "archives":
		return RoleArchive
	case "junk", "spam", "junk e-mail", "bulk mail":
		return RoleJunk
	}
	return ""
}

func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}

// ListMessages lists messages of one mailbox, or of every account's inbox when
// opts.AccountID is empty.
func (s *Service) ListMessages(ctx context.Context, opts ListOptions) (*ListResult, error) {
	limit := opts.Limit
	switch {
	case limit <= 0:
		limit = defaultListLimit
	case limit > maxListLimit:
		limit = maxListLimit
	}
	mailbox := strings.TrimSpace(opts.Mailbox)
	if mailbox == "" {
		mailbox = "INBOX"
	}

	if opts.AccountID != "" {
		a, err := s.account(opts.AccountID)
		if err != nil {
			return nil, err
		}
		return s.listOne(ctx, a, mailbox, limit, opts.BeforeUID, opts.Query)
	}

	s.mu.Lock()
	accounts := s.snapshotLocked()
	s.mu.Unlock()

	type result struct {
		res *ListResult
		err error
	}
	results := make([]result, len(accounts))
	var wg sync.WaitGroup
	for i, a := range accounts {
		wg.Add(1)
		go func(i int, a *MailAccount) {
			defer wg.Done()
			res, err := s.listOne(ctx, a, "INBOX", limit, opts.BeforeUID, opts.Query)
			results[i] = result{res, err}
		}(i, a)
	}
	wg.Wait()

	var all []MailSummary
	hasMore, headersOnly := false, false
	var firstErr error
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		all = append(all, r.res.Messages...)
		hasMore = hasMore || r.res.HasMore
		headersOnly = headersOnly || r.res.HeadersOnly
	}
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date > all[j].Date // RFC3339 sorts lexicographically
		}
		return all[i].UID > all[j].UID
	})
	if len(all) > limit {
		all = all[:limit]
		hasMore = true
	}
	// No NextBeforeUID here: UIDs belong to one mailbox, so there is no single place to continue.
	return &ListResult{Messages: all, HasMore: hasMore, HeadersOnly: headersOnly}, nil
}

// uidSearch is what a search found: the matching UIDs, ascending.
type uidSearch struct {
	uids []imap.UID
	// scannedTo is set when a local search stopped before the oldest message: it is the lowest UID looked at, and
	// the messages below it have not been looked at yet.
	scannedTo imap.UID
	// headersOnly: the server found nothing, so the headers were matched here (searchLocally).
	headersOnly bool
}

// searchUIDs finds the messages below beforeUID that match query (all of them for an empty query). want is how many
// matches the caller can use (a page and one more); only a local search stops early on it, or at the page's
// deadline (localScan.time after started, when the page's IMAP work began).
func (s *Service) searchUIDs(ctx context.Context, c *imapclient.Client, a *MailAccount, mailbox string, beforeUID uint32, query string, want int, started time.Time) (uidSearch, error) {
	if query != "" && a.Provider == ProviderGmail {
		// Gmail query syntax needs X-GM-RAW, which the IMAP client library
		// cannot express; use a short-lived raw connection.
		if uids, err := s.gmailSearch(ctx, a, mailbox, query, beforeUID); err == nil {
			return uidSearch{uids: uids}, nil
		} else {
			logf("mail: X-GM-RAW search failed, falling back to TEXT search: %v", err)
		}
	}
	criteria := belowUID(beforeUID)
	if query != "" {
		criteria.Text = append(criteria.Text, query)
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if query != "" {
		// Not every server searches on its side: NetEase 163 answers every keyword SEARCH (TEXT, BODY, SUBJECT,
		// FROM, any language) with nothing, even for a word in the subject; others refuse some queries with NO.
		// Match the headers here then. A server that does search and found nothing costs one bounded scan.
		var ie *imap.Error
		switch {
		case err == nil && len(data.AllUIDs()) == 0:
			return s.searchLocally(c, beforeUID, query, want, started)
		case errors.As(err, &ie):
			logf("mail: keyword search refused, matching headers locally: %v", err)
			return s.searchLocally(c, beforeUID, query, want, started)
		}
	}
	if err != nil {
		return uidSearch{}, err
	}
	uids := data.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uidSearch{uids: uids}, nil
}

// belowUID selects the messages with a UID lower than beforeUID (all of them for 0).
func belowUID(beforeUID uint32) *imap.SearchCriteria {
	criteria := &imap.SearchCriteria{}
	if beforeUID > 1 {
		var set imap.UIDSet
		set.AddRange(1, imap.UID(beforeUID-1))
		criteria.UID = append(criteria.UID, set)
	}
	return criteria
}

// localScanLimits is s.scan with the defaults filled in (a zero Service has none).
func (s *Service) localScanLimits() localScan {
	l := s.scan
	if l.batch <= 0 {
		l.batch = defaultLocalScan.batch
	}
	if l.budget <= 0 {
		l.budget = defaultLocalScan.budget
	}
	if l.time <= 0 {
		l.time = defaultLocalScan.time
	}
	return l
}

// searchLocally matches query against the envelopes of the messages below beforeUID, newest first, in batches. It
// stops once it has want matches or when the page's budget (messages looked at, time) is spent; scannedTo then says
// where the next page continues.
func (s *Service) searchLocally(c *imapclient.Client, beforeUID uint32, query string, want int, started time.Time) (uidSearch, error) {
	data, err := c.UIDSearch(belowUID(beforeUID), nil).Wait()
	if err != nil {
		return uidSearch{}, err
	}
	all := data.AllUIDs()
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	limits := s.localScanLimits()
	terms := strings.Fields(strings.ToLower(query))
	out := uidSearch{headersOnly: true}
	deadline := started.Add(limits.time)
	looked := 0
	end := len(all) // all[:end] is still to look at
	// The budget is checked after each batch, so every page looks at one batch at least and moves on.
	for end > 0 && len(out.uids) < want {
		n := min(limits.batch, limits.budget-looked, end)
		var set imap.UIDSet
		set.AddNum(all[end-n : end]...)
		msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true}).Collect()
		if err != nil {
			return uidSearch{}, fmt.Errorf("fetch envelopes: %w", err)
		}
		for _, m := range msgs {
			if headerMatch(m.Envelope, terms) {
				out.uids = append(out.uids, m.UID)
			}
		}
		looked += n
		end -= n
		if looked >= limits.budget || time.Now().After(deadline) {
			break
		}
	}
	if end > 0 {
		out.scannedTo = all[end] // the lowest UID looked at
	}
	sort.Slice(out.uids, func(i, j int) bool { return out.uids[i] < out.uids[j] })
	return out, nil
}

// headerMatch reports whether every term occurs, ignoring case, in the subject or in the name or address of a
// sender or recipient.
func headerMatch(env *imap.Envelope, terms []string) bool {
	if env == nil || len(terms) == 0 {
		return false
	}
	var b strings.Builder
	b.WriteString(env.Subject)
	for _, list := range [][]imap.Address{env.From, env.Sender, env.ReplyTo, env.To, env.Cc} {
		for _, addr := range list {
			b.WriteByte('\n')
			b.WriteString(addr.Name)
			b.WriteByte(' ')
			b.WriteString(addr.Addr())
		}
	}
	hay := strings.ToLower(b.String())
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// listOne pages one mailbox of one account.
func (s *Service) listOne(ctx context.Context, a *MailAccount, mailbox string, limit int, beforeUID uint32, query string) (*ListResult, error) {
	var out *ListResult
	err := s.withConnAt(ctx, a, func(c *imapclient.Client, deadline time.Time) error {
		out = nil
		// When the operation began, connecting included (a cold login to a slow server takes seconds), and not again
		// on a retry: the page's time counts from here. A sooner context deadline makes it earlier, which is safe.
		started := deadline.Add(-networkTimeout)
		sel, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		found, err := s.searchUIDs(ctx, c, a, mailbox, beforeUID, query, limit+1, started)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}
		uids := found.uids
		res := &ListResult{Messages: []MailSummary{}, HeadersOnly: found.headersOnly, UIDValidity: sel.UIDValidity}
		switch {
		case len(uids) > limit:
			uids = uids[len(uids)-limit:]
			res.HasMore, res.NextBeforeUID = true, uint32(uids[0])
		case found.scannedTo > 0:
			// A local search ran out of its page's budget: carry on below the last message it looked at.
			res.HasMore, res.NextBeforeUID = true, uint32(found.scannedTo)
		}
		if len(uids) == 0 {
			out = res
			return nil
		}

		// Newest first, a batch at a time: a slow server (NetEase 163 takes seconds for a batch of old mail) could
		// otherwise run the page past its deadline. Once little time is left the page ends here and the rest comes
		// with the next one.
		fetch := s.pageFetchLimits()
		sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
		for i := 0; i < len(uids); i += fetch.batch {
			if i > 0 && time.Now().After(deadline.Add(-fetch.reserve)) {
				res.HasMore, res.NextBeforeUID = true, uint32(uids[i-1])
				break
			}
			var set imap.UIDSet
			set.AddNum(uids[i:min(i+fetch.batch, len(uids))]...)
			msgs, err := c.Fetch(set, &imap.FetchOptions{
				UID:           true,
				Envelope:      true,
				Flags:         true,
				InternalDate:  true,
				RFC822Size:    true,
				BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
				BodySection: []*imap.FetchItemBodySection{
					{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Content-Type", "Content-Transfer-Encoding"}, Peek: true},
					{Specifier: imap.PartSpecifierText, Partial: &imap.SectionPartial{Offset: 0, Size: snippetBytes}, Peek: true},
				},
			}).Collect()
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			for _, m := range msgs {
				res.Messages = append(res.Messages, s.summaryFrom(a, mailbox, m))
			}
		}
		sort.SliceStable(res.Messages, func(i, j int) bool {
			return res.Messages[i].UID > res.Messages[j].UID
		})
		out = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// summaryFrom converts one fetched message into a summary row.
func (s *Service) summaryFrom(a *MailAccount, mailbox string, m *imapclient.FetchMessageBuffer) MailSummary {
	sum := MailSummary{
		AccountID: a.ID,
		Mailbox:   mailbox,
		UID:       uint32(m.UID),
		Seen:      hasFlag(m.Flags, imap.FlagSeen),
		Flagged:   hasFlag(m.Flags, imap.FlagFlagged),
	}
	if m.Envelope != nil {
		sum.Subject = m.Envelope.Subject
		sum.MessageID = m.Envelope.MessageID
		sum.From = firstAddress(m.Envelope.From)
		for _, addr := range m.Envelope.To {
			if e := addr.Addr(); e != "" {
				sum.To = append(sum.To, Address{Name: addr.Name, Email: e})
			}
		}
		date := m.Envelope.Date
		if date.IsZero() {
			date = m.InternalDate
		}
		if !date.IsZero() {
			sum.Date = date.UTC().Format(time.RFC3339)
		}
	} else if !m.InternalDate.IsZero() {
		sum.Date = m.InternalDate.UTC().Format(time.RFC3339)
	}
	sum.HasAttach = bodyStructureHasAttachments(m.BodyStructure)
	sum.Snippet = snippetOf(m.BodySection)
	return sum
}

func firstAddress(addrs []imap.Address) Address {
	for _, addr := range addrs {
		if e := addr.Addr(); e != "" {
			return Address{Name: addr.Name, Email: e}
		}
	}
	return Address{}
}

func hasFlag(flags []imap.Flag, want imap.Flag) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

// bodyStructureHasAttachments reports whether any part is a non-inline
// attachment.
func bodyStructureHasAttachments(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	found := false
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if found {
			return false
		}
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		if disp := single.Disposition(); disp != nil {
			if strings.EqualFold(disp.Value, "attachment") {
				found = true
				return false
			}
			if strings.EqualFold(disp.Value, "inline") {
				return true
			}
		}
		if single.Filename() != "" && !strings.HasPrefix(single.MediaType(), "text/") {
			found = true
			return false
		}
		return true
	})
	return found
}

// snippetOf turns the fetched header + body sections into a short preview.
func snippetOf(sections []imapclient.FetchBodySectionBuffer) string {
	var header, body []byte
	for _, sec := range sections {
		if sec.Section == nil {
			continue
		}
		switch sec.Section.Specifier {
		case imap.PartSpecifierHeader:
			header = sec.Bytes
		case imap.PartSpecifierText:
			body = sec.Bytes
		}
	}
	if len(body) == 0 {
		return ""
	}
	raw := append(append([]byte{}, header...), body...)
	text := textFromRaw(raw)
	if text == "" {
		text = textFromRaw(body) // no usable header: try the body alone
	}
	text = strings.Join(strings.Fields(text), " ")
	return truncateRunes(text, snippetRunes)
}

// textFromRaw extracts readable text from a possibly truncated raw message.
func textFromRaw(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	// Tolerate truncation and unknown charsets: message.Read still returns a
	// usable entity for the leading bytes.
	e, _ := gomessage.Read(bytes.NewReader(raw))
	if e == nil {
		return ""
	}
	return textFromEntity(e, 0)
}

const textFromEntityMaxDepth = 20 // real mail is shallow; a cap stops a crafted nest from blowing the stack

// textFromEntity returns the first non-empty text/* part, depth-first; multipart parts (e.g. alternative inside mixed)
// are descended into.
func textFromEntity(e *gomessage.Entity, depth int) string {
	if depth > textFromEntityMaxDepth {
		return ""
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			p, err := mr.NextPart()
			if gomessage.IsUnknownEncoding(err) {
				continue // a transfer encoding go-message cannot decode: skip the part, the next one is still readable
			}
			// A part in an unknown charset comes back with its raw body, as go-message intends.
			if p == nil || (err != nil && !gomessage.IsUnknownCharset(err)) {
				return "" // end of this multipart (or a truncated tail)
			}
			if text := textFromEntity(p, depth+1); text != "" {
				return text
			}
		}
	}
	mt, _, _ := e.Header.ContentType()
	if !strings.HasPrefix(mt, "text/") {
		return ""
	}
	return renderText(mt, readAllTolerant(e.Body, 8<<10))
}

// readAllTolerant reads at most n bytes, ignoring decode errors at a truncated
// tail.
func readAllTolerant(r io.Reader, n int) string {
	if r == nil {
		return ""
	}
	buf := &bytes.Buffer{}
	_, _ = io.CopyN(buf, r, int64(n))
	return buf.String()
}

// renderText converts text/plain or text/html to preview text.
func renderText(mediaType string, s string) string {
	if strings.HasPrefix(mediaType, "text/html") {
		return stripHTML(s)
	}
	return s
}

// stripHTML removes tags and script/style content and unescapes entities.
func stripHTML(s string) string {
	var b strings.Builder
	lower := asciiLower(s) // same length as s: tags are matched at s's offsets
	i := 0
	for i < len(s) {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		tag := lower[i : i+end+1]
		i += end + 1
		if strings.HasPrefix(tag, "<script") || strings.HasPrefix(tag, "<style") {
			closing := "</script"
			if strings.HasPrefix(tag, "<style") {
				closing = "</style"
			}
			j := strings.Index(lower[i:], closing)
			if j < 0 {
				break // a preview cut off inside a <style> block: the rest is CSS, not text
			}
			i += j
		} else if strings.HasPrefix(tag, "<br") || strings.HasPrefix(tag, "</p") || strings.HasPrefix(tag, "</div") {
			b.WriteByte(' ')
		}
	}
	return html.UnescapeString(b.String())
}

// asciiLower lowers A–Z only, byte for byte. strings.ToLower can change the length (U+0130 and the Kelvin sign lower
// to one byte, an invalid byte becomes U+FFFD's three), and stripHTML indexes the lowered copy with s's offsets.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	rs := []rune(s)
	return string(rs[:n])
}

// GetMessage fetches one full message. It never marks the message as seen.
func (s *Service) GetMessage(ctx context.Context, accountID string, uid uint32, mailbox string) (*MailMessage, error) {
	a, err := s.account(accountID)
	if err != nil {
		return nil, err
	}
	if mailbox == "" {
		mailbox = "INBOX"
	}
	var out *MailMessage
	err = s.withConn(ctx, a, func(c *imapclient.Client) error {
		out = nil
		if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
			UID:          true,
			Envelope:     true,
			Flags:        true,
			InternalDate: true,
			RFC822Size:   true,
			BodySection:  []*imap.FetchItemBodySection{{Peek: true}},
		}).Collect()
		if err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
		if len(msgs) == 0 {
			return fmt.Errorf("message %d not found in %s", uid, mailbox)
		}
		out = messageFrom(a, mailbox, msgs[0])
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// messageFrom parses a fully fetched message.
func messageFrom(a *MailAccount, mailbox string, m *imapclient.FetchMessageBuffer) *MailMessage {
	sum := MailSummary{
		AccountID: a.ID,
		Mailbox:   mailbox,
		UID:       uint32(m.UID),
		Seen:      hasFlag(m.Flags, imap.FlagSeen),
		Flagged:   hasFlag(m.Flags, imap.FlagFlagged),
	}
	if m.Envelope != nil {
		sum.Subject = m.Envelope.Subject
		sum.MessageID = m.Envelope.MessageID
		sum.From = firstAddress(m.Envelope.From)
		for _, addr := range m.Envelope.To {
			if e := addr.Addr(); e != "" {
				sum.To = append(sum.To, Address{Name: addr.Name, Email: e})
			}
		}
		if !m.Envelope.Date.IsZero() {
			sum.Date = m.Envelope.Date.UTC().Format(time.RFC3339)
		}
	}
	if sum.Date == "" && !m.InternalDate.IsZero() {
		sum.Date = m.InternalDate.UTC().Format(time.RFC3339)
	}
	msg := &MailMessage{MailSummary: sum, Attachments: []Attachment{}, References: []string{}, Cc: []Address{}}

	var raw []byte
	for _, sec := range m.BodySection {
		if sec.Section != nil && len(sec.Section.Part) == 0 && sec.Section.Specifier == imap.PartSpecifierNone {
			raw = sec.Bytes
		}
	}
	if raw == nil && len(m.BodySection) > 0 {
		raw = m.BodySection[0].Bytes
	}
	parseMessage(raw, msg)
	msg.HasAttach = len(msg.Attachments) > 0
	msg.Snippet = truncateRunes(strings.Join(strings.Fields(msg.Text), " "), snippetRunes)
	if msg.Snippet == "" {
		msg.Snippet = truncateRunes(strings.Join(strings.Fields(stripHTML(msg.HTML)), " "), snippetRunes)
	}
	return msg
}

// parseMessage fills text, html, attachments and threading headers.
func parseMessage(raw []byte, msg *MailMessage) {
	if len(raw) == 0 {
		return
	}
	// Headers first, so threading info survives a body parse failure.
	if h, err := readMailHeader(raw); err == nil {
		if ids, err := h.MsgIDList("In-Reply-To"); err == nil && len(ids) > 0 {
			msg.InReplyTo = ids[len(ids)-1]
		}
		if ids, err := h.MsgIDList("References"); err == nil && len(ids) > 0 {
			msg.References = ids
		}
		if len(msg.To) == 0 {
			if addrs, err := h.AddressList("To"); err == nil {
				for _, a := range addrs {
					msg.To = append(msg.To, Address{Name: a.Name, Email: a.Address})
				}
			}
		}
		if addrs, err := h.AddressList("Cc"); err == nil {
			for _, a := range addrs {
				msg.Cc = append(msg.Cc, Address{Name: a.Name, Email: a.Address})
			}
		}
	}

	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		_ = err
		return
	}
	defer r.Close()
	var htmlBody string
	attachmentIndex := 0
	for {
		part, err := nextPart(r)
		if err != nil {
			break
		}
		switch h := part.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := h.ContentType()
			body := readAllTolerant(part.Body, 8<<20)
			switch {
			case strings.HasPrefix(ct, "text/plain") && msg.Text == "":
				msg.Text = body
			case strings.HasPrefix(ct, "text/html") && htmlBody == "":
				htmlBody = body
			}
		case *gomail.AttachmentHeader:
			// Downloads count every attachment nextPart returns, including parts whose body cannot be decoded.
			attachmentIndex++
			attachment, err := attachmentMetadata(h, part.Body, attachmentIndex, io.Discard)
			if err == nil {
				msg.Attachments = append(msg.Attachments, attachment)
			}
		}
	}
	if htmlBody != "" {
		msg.HTML = htmlBody
	}
	if msg.Text == "" && msg.HTML != "" {
		msg.Text = stripHTML(msg.HTML)
	}
	if msg.Attachments == nil {
		msg.Attachments = []Attachment{}
	}
}

// readMailHeader parses just the header block of a raw message.
func readMailHeader(raw []byte) (gomail.Header, error) {
	e, err := gomessage.Read(bytes.NewReader(raw))
	if e == nil {
		return gomail.Header{}, err
	}
	return gomail.Header{Header: e.Header}, err
}

// SetFlags updates seen / flagged for one message.
func (s *Service) SetFlags(ctx context.Context, accountID string, uid uint32, req SetFlagsRequest) error {
	a, err := s.account(accountID)
	if err != nil {
		return err
	}
	mailbox := req.Mailbox
	if mailbox == "" {
		mailbox = "INBOX"
	}
	return s.withConn(ctx, a, func(c *imapclient.Client) error {
		if _, err := c.Select(mailbox, nil).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if req.Seen != nil {
			if err := storeFlag(c, set, imap.FlagSeen, *req.Seen); err != nil {
				return err
			}
		}
		if req.Flagged != nil {
			if err := storeFlag(c, set, imap.FlagFlagged, *req.Flagged); err != nil {
				return err
			}
		}
		return nil
	})
}

func storeFlag(c *imapclient.Client, set imap.UIDSet, flag imap.Flag, on bool) error {
	op := imap.StoreFlagsAdd
	if !on {
		op = imap.StoreFlagsDel
	}
	_, err := c.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{flag}}, nil).Collect()
	return err
}

// PlainText is an HTML body as readable text (styles and scripts dropped, whitespace collapsed).
func PlainText(html string) string { return strings.Join(strings.Fields(stripHTML(html)), " ") }

var (
	blockTag   = lazyre.New(`(?i)<\s*(br|/p|/div|/ul|/ol|/tr|/h[1-6]|/blockquote|/table)\b[^>]*>`)
	listItem   = lazyre.New(`(?i)<\s*li\b[^>]*>`)
	spaces     = lazyre.New(`[ \t\x{00a0}]+`)
	blankLines = lazyre.New(`\n{3,}`)
)

// ReadableText is an HTML mail as text that keeps its paragraphs and list items (PlainText flattens everything onto
// one line, fine for a preview but not for reading or translating).
func ReadableText(h string) string {
	h = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(h) // line breaks in the source are only spaces
	h = blockTag.ReplaceAllString(h, "\n$0")
	h = listItem.ReplaceAllString(h, "\n• $0")
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(stripHTML(h), "\r", ""), "\n") {
		lines = append(lines, strings.TrimSpace(spaces.ReplaceAllString(l, " ")))
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
