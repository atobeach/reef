package support

import "strings"

// StripTags keeps only text, dropping tags (quotes inside respected, nested
// `<` counted), comments, `<!DOCTYPE …>` and `<?…?>` blocks; a `<` before
// whitespace is literal.
func StripTags(html string) string {
	s := &tagStripper{html: html}
	for i := range len(html) {
		s.step(i)
	}

	return s.out.String()
}

type stripState int

const (
	inText stripState = iota
	inTag
	inProcessing  // <? … ?>
	inDeclaration // <! … >
	inComment     // <!-- … -->
)

type tagStripper struct {
	html     string
	out      strings.Builder
	state    stripState
	depth    int
	inQuote  byte
	lastChar byte
}

func (s *tagStripper) at(i int) byte {
	if i < 0 || i >= len(s.html) {
		return 0
	}

	return s.html[i]
}

// text keeps c when outside any markup.
func (s *tagStripper) text(c byte) {
	if s.state == inText {
		s.out.WriteByte(c)
	}
}

func (s *tagStripper) step(i int) {
	c := s.html[i]

	switch c {
	case 0:
		// NUL bytes are dropped.
	case '<':
		s.open(i, c)
	case '(', ')':
		s.paren(c)
	case '>':
		s.closeAngle(i, c)
	case '"', '\'':
		s.quote(i, c)
	case '!':
		if s.state == inTag && s.at(i-1) == '<' {
			s.state = inDeclaration
			s.lastChar = c
		} else {
			s.text(c)
		}
	case '-':
		if s.state == inDeclaration && s.at(i-1) == '-' && s.at(i-2) == '!' {
			s.state = inComment
		} else {
			s.text(c)
		}
	case '?':
		if s.state == inTag && s.at(i-1) == '<' {
			s.state = inProcessing
		} else {
			s.text(c)
		}
	case 'E', 'e':
		// <!DOCTYPE …> ends like an ordinary tag.
		if s.state == inDeclaration && i >= 6 && strings.EqualFold(s.html[i-6:i+1], "doctype") {
			s.state = inTag
		} else {
			s.text(c)
		}
	default:
		s.text(c)
	}
}

func (s *tagStripper) open(i int, c byte) {
	if s.inQuote != 0 {
		return
	}

	if isSpace(s.at(i + 1)) {
		s.text(c)

		return
	}

	switch s.state {
	case inText:
		s.lastChar = '<'
		s.state = inTag
	case inTag:
		s.depth++
	default:
	}
}

func (s *tagStripper) paren(c byte) {
	switch s.state {
	case inProcessing:
		if s.lastChar != '"' && s.lastChar != '\'' {
			s.lastChar = c
		}
	case inText:
		s.out.WriteByte(c)
	default:
	}
}

func (s *tagStripper) closeAngle(i int, c byte) {
	if s.depth > 0 {
		s.depth--

		return
	}

	if s.inQuote != 0 {
		return
	}

	switch s.state {
	case inTag:
		s.lastChar = '>'
		s.leave()
	case inProcessing:
		if s.lastChar != '"' && s.at(i-1) == '?' {
			s.leave()
		}
	case inDeclaration:
		s.leave()
	case inComment:
		if s.at(i-1) == '-' && s.at(i-2) == '-' {
			s.leave()
		}
	default:
		s.out.WriteByte(c)
	}
}

func (s *tagStripper) leave() {
	s.state = inText
	s.inQuote = 0
}

func (s *tagStripper) quote(i int, c byte) {
	if s.state == inComment {
		return
	}

	if s.state == inProcessing && s.at(i-1) != '\\' {
		switch {
		case s.lastChar == c:
			s.lastChar = 0
		case s.lastChar != '\\':
			s.lastChar = c
		}
	} else {
		s.text(c)
	}

	if s.state != inText && i > 0 && (s.state == inTag || s.at(i-1) != '\\') && (s.inQuote == 0 || c == s.inQuote) {
		if s.inQuote != 0 {
			s.inQuote = 0
		} else {
			s.inQuote = c
		}
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
