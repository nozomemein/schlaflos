package wake

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseAutoWakePlist extracts the recurring events from the XML form of
// /Library/Preferences/SystemConfiguration/com.apple.AutoWake.plist:
//
//	RepeatingPowerOn  = { eventtype = "wakepoweron", time = 480, weekdays = 127 }
//	RepeatingPowerOff = { eventtype = "shutdown",    time = 1320, weekdays = 31 }
//
// Only these keys are read; everything else in the file is ignored.
func ParseAutoWakePlist(data []byte) (Schedule, error) {
	root, err := decodePlist(data)
	if err != nil {
		return Schedule{}, err
	}
	top, ok := root.(map[string]any)
	if !ok {
		return Schedule{}, fmt.Errorf("parse AutoWake plist: top-level value is not a dict")
	}
	var s Schedule
	if ev, err := plistEvent(top["RepeatingPowerOn"]); err != nil {
		return Schedule{}, err
	} else if ev != nil {
		s.On = ev
	}
	if ev, err := plistEvent(top["RepeatingPowerOff"]); err != nil {
		return Schedule{}, err
	} else if ev != nil {
		s.Off = ev
	}
	return s, nil
}

func plistEvent(v any) (*Event, error) {
	if v == nil {
		return nil, nil
	}
	d, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse AutoWake plist: repeating event is not a dict")
	}
	rawType, _ := d["eventtype"].(string)
	typ, err := normaliseType(rawType)
	if err != nil {
		return nil, fmt.Errorf("parse AutoWake plist: %w", err)
	}
	minutes, ok := d["time"].(int64)
	if !ok || minutes < 0 || minutes >= 24*60 {
		return nil, fmt.Errorf("parse AutoWake plist: invalid time %v", d["time"])
	}
	days, ok := d["weekdays"].(int64)
	if !ok || days <= 0 || days > int64(EveryDay) {
		return nil, fmt.Errorf("parse AutoWake plist: invalid weekdays %v", d["weekdays"])
	}
	return &Event{Type: typ, Minutes: int(minutes), Days: DayMask(days), DaysExact: true}, nil
}

// decodePlist is a minimal XML property-list decoder covering the value
// types powerd writes. Dictionaries become map[string]any, arrays []any,
// integers int64, reals float64, booleans bool, and everything else string.
func decodePlist(data []byte) (any, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = true
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse plist: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "plist" {
			return nil, fmt.Errorf("parse plist: unexpected root element %q", se.Name.Local)
		}
		return decodeValue(dec, nextStart(dec))
	}
}

func nextStart(dec *xml.Decoder) *xml.StartElement {
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return &t
		case xml.EndElement:
			return nil
		}
	}
}

func decodeValue(dec *xml.Decoder, se *xml.StartElement) (any, error) {
	if se == nil {
		return nil, fmt.Errorf("parse plist: missing value")
	}
	switch se.Name.Local {
	case "dict":
		out := map[string]any{}
		for {
			keyEl := nextStart(dec)
			if keyEl == nil {
				return out, nil
			}
			if keyEl.Name.Local != "key" {
				return nil, fmt.Errorf("parse plist: expected key, got %q", keyEl.Name.Local)
			}
			key, err := text(dec, keyEl)
			if err != nil {
				return nil, err
			}
			val, err := decodeValue(dec, nextStart(dec))
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
	case "array":
		var out []any
		for {
			el := nextStart(dec)
			if el == nil {
				return out, nil
			}
			val, err := decodeValue(dec, el)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	case "integer":
		s, err := text(dec, se)
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse plist: bad integer %q", s)
		}
		return n, nil
	case "real":
		s, err := text(dec, se)
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("parse plist: bad real %q", s)
		}
		return f, nil
	default: // string, date, data
		return text(dec, se)
	}
}

func text(dec *xml.Decoder, se *xml.StartElement) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("parse plist: unterminated %s", se.Name.Local)
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("parse plist: unexpected element %q inside %s", t.Name.Local, se.Name.Local)
		}
	}
}
