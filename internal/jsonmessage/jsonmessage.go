// Copyright 2014 Docker authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the DOCKER-LICENSE file.

package jsonmessage

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/fsouza/go-dockerclient/internal/term"
	"github.com/ijc/Gotty"
)

// RFC3339NanoFixed is time.RFC3339Nano with nanoseconds padded using zeros to
// ensure the formatted time isalways the same number of characters.
const RFC3339NanoFixed = "2006-01-02T15:04:05.000000000Z07:00"

// JSONError wraps a concrete Code and Message, `Code` is
// is an integer error code, `Message` is the error message.
type JSONError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e *JSONError) Error() string {
	return e.Message
}

// JSONProgress describes a Progress. terminalFd is the fd of the current terminal,
// Start is the initial value for the operation. Current is the current status and
// value of the progress made towards Total. Total is the end value describing when
// we made 100% progress for an operation.
type JSONProgress struct {
	terminalFd uintptr
	Current    int64 `json:"current,omitempty"`
	Total      int64 `json:"total,omitempty"`
	Start      int64 `json:"start,omitempty"`
	// If true, don't show xB/yB
	HideCounts bool   `json:"hidecounts,omitempty"`
	Units      string `json:"units,omitempty"`
	nowFunc    func() time.Time
	winSize    int
}

func (p *JSONProgress) String() string {
	var (
		width       = p.width()
		pbBox       string
		numbersBox  string
		timeLeftBox string
	)
	if p.Current <= 0 && p.Total <= 0 {
		return ""
	}
	if p.Total <= 0 {
		switch p.Units {
		case "":
			current := units.HumanSize(float64(p.Current))
			return fmt.Sprintf("%8v", current)
		default:
			return fmt.Sprintf("%d %s", p.Current, p.Units)
		}
	}

	percentage := int(float64(p.Current)/float64(p.Total)*100) / 2
	if percentage > 50 {
		percentage = 50
	}
	if width > 110 {
		// this number can't be negative gh#7136
		numSpaces := 0
		if 50-percentage > 0 {
			numSpaces = 50 - percentage
		}
		pbBox = fmt.Sprintf("[%s>%s] ", strings.Repeat("=", percentage), strings.Repeat(" ", numSpaces))
	}

	switch {
	case p.HideCounts:
	case p.Units == "": // no units, use bytes
		current := units.HumanSize(float64(p.Current))
		total := units.HumanSize(float64(p.Total))

		numbersBox = fmt.Sprintf("%8v/%v", current, total)

		if p.Current > p.Total {
			// remove total display if the reported current is wonky.
			numbersBox = fmt.Sprintf("%8v", current)
		}
	default:
		numbersBox = fmt.Sprintf("%d/%d %s", p.Current, p.Total, p.Units)

		if p.Current > p.Total {
			// remove total display if the reported current is wonky.
			numbersBox = fmt.Sprintf("%d %s", p.Current, p.Units)
		}
	}

	if p.Current > 0 && p.Start > 0 && percentage < 50 {
		fromStart := p.now().Sub(time.Unix(p.Start, 0))
		perEntry := fromStart / time.Duration(p.Current)
		left := time.Duration(p.Total-p.Current) * perEntry
		left = (left / time.Second) * time.Second

		if width > 50 {
			timeLeftBox = " " + left.String()
		}
	}
	return pbBox + numbersBox + timeLeftBox
}

// now; shim for testing
func (p *JSONProgress) now() time.Time {
	if p.nowFunc == nil {
		p.nowFunc = func() time.Time {
			return time.Now().UTC()
		}
	}
	return p.nowFunc()
}

// width; shim for testing
func (p *JSONProgress) width() int {
	if p.winSize != 0 {
		return p.winSize
	}
	ws, err := term.GetWinsize(p.terminalFd)
	if err == nil {
		return int(ws.Width)
	}
	return 200
}

// JSONMessage defines a message struct. It describes
// the created time, where it from, status, ID of the
// message. It's used for docker events.
type JSONMessage struct {
	Stream          string        `json:"stream,omitempty"`
	Status          string        `json:"status,omitempty"`
	Progress        *JSONProgress `json:"progressDetail,omitempty"`
	ProgressMessage string        `json:"progress,omitempty"` //deprecated
	ID              string        `json:"id,omitempty"`
	From            string        `json:"from,omitempty"`
	Time            int64         `json:"time,omitempty"`
	TimeNano        int64         `json:"timeNano,omitempty"`
	Error           *JSONError    `json:"errorDetail,omitempty"`
	ErrorMessage    string        `json:"error,omitempty"` //deprecated
	// Aux contains out-of-band data, such as digests for push signing and image id after building.
	Aux *json.RawMessage `json:"aux,omitempty"`
}

/* Satisfied by gotty.TermInfo as well as noTermInfo from below */
type termInfo interface {
	Parse(attr string, params ...interface{}) (string, error)
}

type noTermInfo struct{} // canary used when no terminfo.

func (ti *noTermInfo) Parse(attr string, params ...interface{}) (string, error) {
	return "", fmt.Errorf("noTermInfo")
}

func clearLine(out io.Writer, ti termInfo) error {
	// el2 (clear whole line) is not exposed by terminfo.

	// First clear line from beginning to cursor
	if attr, err := ti.Parse("el1"); err == nil {
		_, err = fmt.Fprintf(out, "%s", attr)
		if err != nil {
			return err
		}
	} else {
		_, err := fmt.Fprintf(out, "\x1b[1K")
		if err != nil {
			return err
		}
	}
	// Then clear line from cursor to end
	if attr, err := ti.Parse("el"); err == nil {
		_, err = fmt.Fprintf(out, "%s", attr)
		if err != nil {
			return err
		}
	} else {
		_, err := fmt.Fprintf(out, "\x1b[K")
		if err != nil {
			return err
		}
	}

	return nil
}

func cursorUp(out io.Writer, ti termInfo, l int) error {
	if l == 0 { // Should never be the case, but be tolerant
		return nil
	}
	if attr, err := ti.Parse("cuu", l); err == nil {
		_, err = fmt.Fprintf(out, "%s", attr)
		if err != nil {
			return err
		}
	} else {
		_, err := fmt.Fprintf(out, "\x1b[%dA", l)
		if err != nil {
			return err
		}
	}
	return nil
}

func cursorDown(out io.Writer, ti termInfo, l int) error {
	if l == 0 { // Should never be the case, but be tolerant
		return nil
	}
	if attr, err := ti.Parse("cud", l); err == nil {
		_, err = fmt.Fprintf(out, "%s", attr)
		if err != nil {
			return err
		}
	} else {
		_, err := fmt.Fprintf(out, "\x1b[%dB", l)
		if err != nil {
			return err
		}
	}

	return nil
}

// Display displays the JSONMessage to `out`. `termInfo` is non-nil if `out`
// is a terminal. If this is the case, it will erase the entire current line
// when displaying the progressbar.
func (jm *JSONMessage) Display(out io.Writer, termInfo termInfo) error {
	if jm.Error != nil {
		if jm.Error.Code == 401 {
			return fmt.Errorf("authentication is required")
		}
		return jm.Error
	}
	var endl string
	if termInfo != nil && jm.Stream == "" && jm.Progress != nil {
		clearLine(out, termInfo)
		endl = "\r"
		_, err := fmt.Fprint(out, endl)
		if err != nil {
			return err
		}
	} else if jm.Progress != nil && jm.Progress.String() != "" { //disable progressbar in non-terminal
		return nil
	}
	if jm.TimeNano != 0 {
		_, err := fmt.Fprintf(out, "%s ", time.Unix(0, jm.TimeNano).Format(RFC3339NanoFixed))
		if err != nil {
			return err
		}
	} else if jm.Time != 0 {
		_, err := fmt.Fprintf(out, "%s ", time.Unix(jm.Time, 0).Format(RFC3339NanoFixed))
		if err != nil {
			return err
		}
	}
	if jm.ID != "" {
		_, err := fmt.Fprintf(out, "%s: ", jm.ID)
		if err != nil {
			return err
		}
	}
	if jm.From != "" {
		_, err := fmt.Fprintf(out, "(from %s) ", jm.From)
		if err != nil {
			return err
		}
	}
	if jm.Progress != nil && termInfo != nil {
		_, err := fmt.Fprintf(out, "%s %s%s", jm.Status, jm.Progress.String(), endl)
		if err != nil {
			return err
		}
	} else if jm.ProgressMessage != "" { //deprecated
		_, err := fmt.Fprintf(out, "%s %s%s", jm.Status, jm.ProgressMessage, endl)
		if err != nil {
			return err
		}
	} else if jm.Stream != "" {
		_, err := fmt.Fprintf(out, "%s%s", jm.Stream, endl)
		if err != nil {
			return err
		}
	} else {
		_, err := fmt.Fprintf(out, "%s%s\n", jm.Status, endl)
		if err != nil {
			return err
		}
	}
	return nil
}

// DisplayJSONMessagesStream displays a json message stream from `in` to `out`, `isTerminal`
// describes if `out` is a terminal. If this is the case, it will print `\n` at the end of
// each line and move the cursor while displaying.
func DisplayJSONMessagesStream(in io.Reader, out io.Writer, terminalFd uintptr, isTerminal bool, auxCallback func(JSONMessage)) error {
	var (
		dec = json.NewDecoder(in)
		ids = make(map[string]int)
	)

	var termInfo termInfo

	if isTerminal {
		term := os.Getenv("TERM")
		if term == "" {
			term = "vt102"
		}

		var err error
		if termInfo, err = gotty.OpenTermInfo(term); err != nil {
			termInfo = &noTermInfo{}
		}
	}

	for {
		diff := 0
		var jm JSONMessage
		if err := dec.Decode(&jm); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if jm.Aux != nil {
			if auxCallback != nil {
				auxCallback(jm)
			}
			continue
		}

		if jm.Progress != nil {
			jm.Progress.terminalFd = terminalFd
		}
		if jm.ID != "" && (jm.Progress != nil || jm.ProgressMessage != "") {
			line, ok := ids[jm.ID]
			if !ok {
				// NOTE: This approach of using len(id) to
				// figure out the number of lines of history
				// only works as long as we clear the history
				// when we output something that's not
				// accounted for in the map, such as a line
				// with no ID.
				line = len(ids)
				ids[jm.ID] = line
				if termInfo != nil {
					_, err := fmt.Fprintf(out, "\n")
					if err != nil {
						return err
					}
				}
			}
			diff = len(ids) - line
			if termInfo != nil {
				if err := cursorUp(out, termInfo, diff); err != nil {
					return err
				}
			}
		} else {
			// When outputting something that isn't progress
			// output, clear the history of previous lines. We
			// don't want progress entries from some previous
			// operation to be updated (for example, pull -a
			// with multiple tags).
			ids = make(map[string]int)
		}
		err := jm.Display(out, termInfo)
		if jm.ID != "" && termInfo != nil {
			if err := cursorDown(out, termInfo, diff); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type stream interface {
	io.Writer
	FD() uintptr
	IsTerminal() bool
}

// DisplayJSONMessagesToStream prints json messages to the output stream
func DisplayJSONMessagesToStream(in io.Reader, stream stream, auxCallback func(JSONMessage)) error {
	return DisplayJSONMessagesStream(in, stream, stream.FD(), stream.IsTerminal(), auxCallback)
}
