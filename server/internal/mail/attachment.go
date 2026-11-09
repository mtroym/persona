package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

// attachmentMetadata binds IDs to MIME order, metadata and decoded content.
func attachmentMetadata(h *gomail.AttachmentHeader, body io.Reader, index int, dst io.Writer) (Attachment, error) {
	name, _ := h.Filename()
	ct, _, _ := h.ContentType()
	sum := sha256.New()
	fmt.Fprintf(sum, "%d\x00%s\x00%s\x00", index, name, ct)
	n, err := io.Copy(io.MultiWriter(sum, dst), body)
	return Attachment{ID: strconv.Itoa(index) + "-" + hex.EncodeToString(sum.Sum(nil)), Filename: name, Mime: ct, Size: n}, err
}

// nextPart is r.NextPart past the parts go-message can only partly decode. A part in an unknown charset comes back
// with its raw body and is used as is; a part in an unknown transfer encoding comes back without a header and is
// skipped. The message list and the attachment download both read parts through it, so they count attachment
// ordinals the same way. io.EOF and every other error are returned.
func nextPart(r *gomail.Reader) (*gomail.Part, error) {
	for {
		part, err := r.NextPart()
		switch {
		case gomessage.IsUnknownEncoding(err):
			continue
		case gomessage.IsUnknownCharset(err) && part != nil:
			return part, nil
		}
		return part, err
	}
}

func (s *Service) attachment(ctx context.Context, accountID string, uid uint32, mailbox, id string) (*os.File, Attachment, error) {
	a, err := s.account(accountID)
	if err != nil {
		return nil, Attachment{}, err
	}
	indexText, digest, ok := strings.Cut(id, "-")
	index, parseErr := strconv.Atoi(indexText)
	if !ok || parseErr != nil || index < 1 || len(digest) != 64 {
		return nil, Attachment{}, fmt.Errorf("invalid attachment id")
	}
	if mailbox == "" {
		mailbox = "INBOX"
	}
	file, err := os.CreateTemp("", "persona-mail-attachment-*")
	if err != nil {
		return nil, Attachment{}, err
	}
	var metadata Attachment
	err = s.withConn(ctx, a, func(c *imapclient.Client) error {
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return err
		}
		// BODY.PEEK is available in IMAP4rev1; do not assume BINARY or any LIST extension.
		fetch := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{Peek: true}}})
		defer fetch.Close()
		found := false
		for message := fetch.Next(); message != nil; message = fetch.Next() {
			for item := message.Next(); item != nil; item = message.Next() {
				section, ok := item.(imapclient.FetchItemDataBodySection)
				if !ok || section.Literal == nil {
					continue
				}
				reader, err := gomail.CreateReader(section.Literal)
				if reader == nil {
					return err
				}
				count := 0
				for {
					part, err := nextPart(reader)
					if err == io.EOF {
						break
					}
					if err != nil {
						return err
					}
					header, ok := part.Header.(*gomail.AttachmentHeader)
					if !ok {
						continue
					}
					count++
					if count != index {
						continue
					}
					metadata, err = attachmentMetadata(header, part.Body, count, file)
					if err != nil {
						return err
					}
					if metadata.ID != id {
						return fmt.Errorf("attachment not found")
					}
					found = true
					break
				}
			}
		}
		if err := fetch.Close(); err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("attachment not found")
		}
		return nil
	})
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, Attachment{}, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, Attachment{}, err
	}
	return file, metadata, nil
}

// MaxSavedAttachment bounds an attachment FetchAttachment hands over for mail_save_attachment: it crosses the node's
// connection in one message (base64 in JSON, about 4/3 of it, under the connection's 64 MiB line), and 25 MB is the
// most most providers let a mail carry.
const MaxSavedAttachment = 25 << 20

// ErrAttachmentTooLarge: the attachment is over FetchAttachment's limit.
var ErrAttachmentTooLarge = errors.New("attachment too large")

// FetchAttachment is one attachment's metadata and decoded bytes (the id mail_read lists and the HTTP route takes),
// at most max bytes. Like the download, it does not mark the message as read.
func (s *Service) FetchAttachment(ctx context.Context, accountID string, uid uint32, mailbox, id string, max int64) (Attachment, []byte, error) {
	if uid == 0 {
		return Attachment{}, nil, fmt.Errorf("invalid uid")
	}
	file, metadata, err := s.attachment(ctx, accountID, uid, mailbox, id)
	if err != nil {
		return Attachment{}, nil, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if metadata.Size > max {
		return metadata, nil, fmt.Errorf("%w: %d bytes, the limit is %d", ErrAttachmentTooLarge, metadata.Size, max)
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return Attachment{}, nil, err
	}
	if int64(len(data)) != metadata.Size {
		return Attachment{}, nil, fmt.Errorf("attachment changed while reading")
	}
	return metadata, data, nil
}

func (s *Service) handleAttachment(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil || uid == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid uid"))
		return
	}
	file, metadata, err := s.attachment(r.Context(), r.PathValue("aid"), uint32(uid), r.URL.Query().Get("mailbox"), r.PathValue("attachment"))
	if err != nil {
		s.fail(w, err)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	ct := metadata.Mime
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": metadata.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+metadata.ID+`"`)
	// ServeContent handles Range, If-Range, Content-Range and 416 over decoded bytes.
	http.ServeContent(w, r, metadata.Filename, time.Time{}, file)
}
