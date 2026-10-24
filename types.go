package chronos

import (
	"net/url"
	"strconv"
	"time"
)

// IntervalUnit is the unit of an [Interval].
type IntervalUnit string

const (
	IntervalUnitSecond IntervalUnit = "second"
	IntervalUnitMinute IntervalUnit = "minute"
	IntervalUnitHour   IntervalUnit = "hour"
	IntervalUnitDay    IntervalUnit = "day"
	IntervalUnitWeek   IntervalUnit = "week"
)

// Interval is a length of time such as 10 minutes. Value is a positive
// integer.
type Interval struct {
	Value int          `json:"value"`
	Unit  IntervalUnit `json:"unit"`
}

// DeliveryType says how a job reaches your code.
type DeliveryType string

const (
	// DeliveryTypePull jobs wait for a worker to claim them.
	DeliveryTypePull DeliveryType = "pull"
	// DeliveryTypePush jobs are sent to your endpoint over HTTP.
	DeliveryTypePush DeliveryType = "push"
)

// Delivery says how jobs reach your code. HTTP is set only for push.
type Delivery struct {
	Type DeliveryType  `json:"type"`
	HTTP *HTTPDelivery `json:"http,omitzero"`
}

// HTTPDelivery is the endpoint push delivery calls.
type HTTPDelivery struct {
	URL string `json:"url"`
	// Method is GET, POST, PUT, PATCH or DELETE.
	Method string `json:"method"`
	// Headers are sent with every delivery. The API returns them as stored,
	// unmasked.
	Headers map[string]string `json:"headers,omitzero"`
}

// Paging describes where a [Page] sits in a list.
type Paging struct {
	// Page starts at 1.
	Page int `json:"page"`
	// Pages is 0 for an empty list.
	Pages int `json:"pages"`
	Size  int `json:"size"`
	Total int `json:"total"`
}

// Page is one page of a list, newest first.
type Page[T any] struct {
	Data   []T    `json:"data"`
	Paging Paging `json:"paging"`
}

type query url.Values

func (q query) set(key, value string) {
	if value != "" {
		url.Values(q).Set(key, value)
	}
}

func (q query) setInt(key string, value int) {
	if value != 0 {
		url.Values(q).Set(key, strconv.Itoa(value))
	}
}

func (q query) setTime(key string, value time.Time) {
	if !value.IsZero() {
		url.Values(q).Set(key, value.UTC().Format(time.RFC3339Nano))
	}
}
