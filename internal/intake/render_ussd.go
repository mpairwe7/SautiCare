package intake

import (
	"strconv"
	"strings"
)

// RenderUSSD renders a prompt as USSD text: the notice (on a re-prompt), the
// question and one numbered line per option. end reports a closing screen.
func RenderUSSD(p Prompt) (text string, end bool) {
	var lines []string
	if p.Notice != "" {
		lines = append(lines, p.Notice)
	}
	lines = append(lines, p.Text)
	for _, o := range p.Options {
		lines = append(lines, strconv.Itoa(o.Digit)+" "+o.Label)
	}
	return strings.Join(lines, "\n"), p.End
}
