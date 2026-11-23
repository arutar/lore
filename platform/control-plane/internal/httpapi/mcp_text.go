package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
)

const (
	contentText      = "text"
	contentOtherText = "text-not-utf8"
	contentBinary    = "binary"

	sniffBytes      = 8 << 10
	defaultReadRows = 2000
	maxReadLines    = 10000
	maxMatches      = 200
	maxPattern      = 500
)

// classifyContent decides from a file's first bytes what it holds, as git does: a NUL byte makes it
// binary; a byte-order mark makes it UTF-8 or UTF-16 text; valid UTF-8 (ASCII included) is text;
// anything else without NUL bytes is text in some other encoding, which isn't guessed. complete
// says the bytes are the whole file, so a character cut at the end isn't forgiven.
func classifyContent(head []byte, complete bool) (kind, encodingName string) {
	switch {
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		return contentText, "utf-8"
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}):
		return contentText, "utf-16le"
	case bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		return contentText, "utf-16be"
	case bytes.IndexByte(head, 0) >= 0:
		return contentBinary, ""
	}
	if utf8.Valid(head) {
		return contentText, "utf-8"
	}
	if !complete {
		// The sample may end inside a character.
		for cut := 1; cut < utf8.UTFMax && cut < len(head); cut++ {
			if utf8.Valid(head[:len(head)-cut]) {
				return contentText, "utf-8"
			}
		}
	}
	return contentOtherText, ""
}

// sniffFile reads the start of a file to classify it.
func sniffFile(ctx context.Context, content RepositoryContent, repositoryID, revision string, node loreclient.TreeNode) (string, string, error) {
	if node.Size == 0 {
		return contentText, "utf-8", nil
	}
	download, err := content.Download(ctx, repositoryID, revision, node.Path)
	if err != nil {
		return "", "", err
	}
	defer download.Body.Close()
	head := make([]byte, sniffBytes)
	read, err := io.ReadFull(download.Body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", "", err
	}
	kind, encodingName := classifyContent(head[:read], uint64(read) >= node.Size || read < sniffBytes)
	return kind, encodingName, nil
}

// textEncoding finds an encoding by its usual name or label: windows-1251, cp1251, ibm866, cp866,
// koi8-r, iso-8859-5, utf-16le and the other WHATWG encodings.
func textEncoding(name string) (encoding.Encoding, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "cp866", "866", "dos-866":
		name = "ibm866"
	case "cp1251", "win1251", "windows1251":
		name = "windows-1251"
	case "utf16le":
		name = "utf-16le"
	case "utf16be":
		name = "utf-16be"
	}
	return htmlindex.Get(name)
}

type readTextInput struct {
	Repository, Path, Revision string
	Offset, Limit              int
	Pattern                    string
	Regex                      bool
	Encoding                   string
}

// lineReader returns a file's lines one by one, cutting any line longer than max bytes.
type lineReader struct {
	reader *bufio.Reader
	max    int
}

func (l *lineReader) next() (line string, cut bool, err error) {
	var buffer []byte
	for {
		chunk, err := l.reader.ReadSlice('\n')
		if len(buffer) < l.max {
			room := l.max - len(buffer)
			if len(chunk) > room {
				buffer = append(buffer, chunk[:room]...)
				cut = true
			} else {
				buffer = append(buffer, chunk...)
			}
		} else if len(chunk) > 0 {
			cut = true
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && len(buffer) == 0 {
			return "", false, err
		}
		text := strings.TrimRight(string(buffer), "\r\n")
		return text, cut, nil
	}
}

func toolReadText(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input readTextInput
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	if input.Offset == 0 {
		input.Offset = 1
	}
	if input.Limit == 0 {
		input.Limit = defaultReadRows
	}
	if input.Offset < 1 || input.Limit < 1 || input.Limit > maxReadLines {
		return toolError(fmt.Sprintf("offset must be 1 or more and limit between 1 and %d", maxReadLines))
	}
	match, problem := lineMatcher(input.Pattern, input.Regex)
	if problem != nil {
		return problem
	}
	var decoder encoding.Encoding
	if input.Encoding != "" {
		var err error
		if decoder, err = textEncoding(input.Encoding); err != nil {
			return toolError("Unknown encoding " + strconv.Quote(input.Encoding) + ". Use a name such as windows-1251, ibm866, koi8-r, iso-8859-5 or utf-16le.")
		}
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	content, problem := s.mcpContent()
	if problem != nil {
		return problem
	}
	filePath := cleanPath(input.Path)
	node, revision, err := mcpFile(ctx, content, repository.ID, input.Revision, filePath)
	if err != nil {
		return toolContentError(ctx, err)
	}
	if node.Type != "file" {
		return toolError(filePath + " is a " + node.Type + ", not a file")
	}
	download, err := content.Download(ctx, repository.ID, revision, node.Path)
	if err != nil {
		return toolContentError(ctx, err)
	}
	defer download.Body.Close()
	buffered := bufio.NewReaderSize(download.Body, 64<<10)
	head, _ := buffered.Peek(sniffBytes)
	kind, detected := classifyContent(head, uint64(len(head)) >= node.Size || len(head) < sniffBytes)
	var reader io.Reader = buffered
	encodingName := detected
	switch {
	case decoder != nil:
		reader = transform.NewReader(buffered, decoder.NewDecoder())
		encodingName = strings.ToLower(strings.TrimSpace(input.Encoding))
	case kind == contentBinary:
		return toolError(filePath + " is a binary file and can't be read as text. Use download_link to download it.")
	case kind == contentOtherText:
		return toolError(filePath + " is text, but not UTF-8. Call read_text again with its encoding, such as windows-1251 (Russian Windows), ibm866 (DOS) or koi8-r, or use download_link.")
	default:
		// UTF-8, or UTF-16 with its byte-order mark: the mark picks the decoding and is dropped.
		reader = transform.NewReader(buffered, unicode.BOMOverride(transform.Nop))
	}
	maxLine := min(s.config.MCP.MaxRead, 64<<10)
	lines := &lineReader{reader: bufio.NewReaderSize(reader, 64<<10), max: maxLine}

	var out strings.Builder
	fmt.Fprintf(&out, "%s/%s · revision %s · %d bytes · %s\n", repository.Name, node.Path, revision, node.Size, encodingName)
	shown, matches, number, lastShown, invalid := 0, 0, 0, 0, false
	full := false
	var stopped error
	for {
		if number%1000 == 0 && ctx.Err() != nil {
			stopped = ctx.Err()
			break
		}
		line, cut, err := lines.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				stopped = ctx.Err()
				break
			}
			return toolContentError(ctx, err)
		}
		number++
		if number < input.Offset {
			continue
		}
		if !utf8.ValidString(line) {
			line, invalid = strings.ToValidUTF8(line, "�"), true
		}
		if match != nil {
			if !match(line) {
				continue
			}
			matches++
			if full || shown == maxMatches || shown == input.Limit {
				full = true
				continue // keep counting matches
			}
		} else if shown == input.Limit {
			full = true
			number-- // this line wasn't shown: continue from it
			break
		}
		row := fmt.Sprintf("%6d│ %s", number, line)
		if cut {
			row += fmt.Sprintf(" … [line cut at %d bytes]", maxLine)
		}
		if out.Len()+len(row)+1 > s.config.MCP.MaxRead && shown > 0 {
			full = true
			if match == nil {
				number-- // this line wasn't shown: continue from it
				break
			}
			continue
		}
		out.WriteString(row)
		out.WriteByte('\n')
		shown++
		lastShown = number
	}
	switch {
	case stopped != nil:
		fmt.Fprintf(&out, "[stopped at line %d: the call took too long. Continue with offset=%d.]\n", number, number+1)
	case match != nil && full:
		fmt.Fprintf(&out, "[%d matching lines from line %d, %d shown. Narrow the pattern, or see the rest with offset=%d.]\n", matches, input.Offset, shown, lastShown+1)
	case match != nil:
		fmt.Fprintf(&out, "[%d matching lines from line %d to the end, line %d.]\n", matches, input.Offset, number)
	case full:
		fmt.Fprintf(&out, "[More lines follow. Continue with offset=%d.]\n", number+1)
	default:
		fmt.Fprintf(&out, "[End of file: %d lines.]\n", number)
	}
	if invalid {
		out.WriteString("[Some lines weren't valid in this encoding; their bad bytes show as �. If the text looks wrong, give the right encoding.]\n")
	}
	return toolText(out.String())
}

// lineMatcher returns the test a line must pass, or nil without a pattern.
func lineMatcher(pattern string, regex bool) (func(string) bool, map[string]any) {
	if pattern == "" {
		if regex {
			return nil, toolError("regex needs a pattern")
		}
		return nil, nil
	}
	if len(pattern) > maxPattern {
		return nil, toolError(fmt.Sprintf("pattern may be at most %d characters", maxPattern))
	}
	if regex {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return nil, toolError("Invalid regular expression: " + err.Error())
		}
		return expression.MatchString, nil
	}
	needle := strings.ToLower(pattern)
	return func(line string) bool { return strings.Contains(strings.ToLower(line), needle) }, nil
}
