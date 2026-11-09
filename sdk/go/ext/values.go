package ext

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Reading what an action carries (r.Values) as UI version 6 writes it: a switch "true" or "false", a slider's or a
// stepper's number, a date YYYY-MM-DD, a time HH:MM, a datetime YYYY-MM-DDTHH:MM in the user's time zone (r.TZ), a
// multichoice a JSON array. The hub turns what an older app sends (a multichoice as toggles) into the same.

// Bool is a switch's or a toggle's value: true for "true" only.
func (r *UIRequest) Bool(name string) bool { return r.Values[name] == "true" }

// Number is a slider's, a stepper's or a number field's value; an error when it is missing or not a number.
func (r *UIRequest) Number(name string) (float64, error) {
	s, ok := r.Values[name]
	if !ok || strings.TrimSpace(s) == "" {
		return 0, fmt.Errorf("%s: no value", name)
	}
	x, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("%s: not a number: %q", name, s)
	}
	return x, nil
}

// Location is the user's time zone (r.TZ), UTC when the hub sent none or one this machine does not know.
func (r *UIRequest) Location() *time.Location {
	if r.TZ != "" {
		if loc, err := time.LoadLocation(r.TZ); err == nil {
			return loc
		}
	}
	return time.UTC
}

func (r *UIRequest) parse(name, layout string) (time.Time, error) {
	s := strings.TrimSpace(r.Values[name])
	if s == "" {
		return time.Time{}, fmt.Errorf("%s: no value", name)
	}
	t, err := time.ParseInLocation(layout, s, r.Location())
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: written %s, not %q", name, layout, s)
	}
	return t, nil
}

// Date is a date field's day, at midnight in the user's time zone.
func (r *UIRequest) Date(name string) (time.Time, error) { return r.parse(name, wire.DateLayout) }

// Clock is a time field's hour and minute (24 hours).
func (r *UIRequest) Clock(name string) (hour, minute int, err error) {
	t, err := r.parse(name, wire.ClockLayout)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}

// Time is a datetime field's moment, read in the user's time zone (the field has no offset).
func (r *UIRequest) Time(name string) (time.Time, error) { return r.parse(name, wire.DateTimeLayout) }

// Multi is a multichoice's chosen values, in the order sent; none when it is empty or not a JSON array of strings.
func (r *UIRequest) Multi(name string) []string {
	var vs []string
	if json.Unmarshal([]byte(r.Values[name]), &vs) != nil {
		return nil
	}
	return vs
}
