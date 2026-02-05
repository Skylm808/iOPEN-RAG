package model

import (
	"fmt"
	"time"
)

// LocalTime is a custom time type to format time as "YYYY-MM-DD HH:MM:SS".
type LocalTime time.Time

const timeFormat = "2006-01-02 15:04:05"

// MarshalJSON implements the json.Marshaler interface.
func (t LocalTime) MarshalJSON() ([]byte, error) {
	formatted := fmt.Sprintf("\"%s\"", time.Time(t).Format(timeFormat))
	return []byte(formatted), nil
}
