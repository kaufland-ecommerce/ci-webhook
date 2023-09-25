package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"strings"
)

// ArgumentError describes an invalid argument passed to Hook.
type ArgumentError struct {
	Argument Argument
	err      error
}

// Unwrap returns the underlying error. Implements errors.Unwrap.
func (e *ArgumentError) Unwrap() error {
	return e.err
}

func (e *ArgumentError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("couldn't retrieve argument for %+v: %s", e.Argument, e.err)
}

// Argument type specifies the parameter key name and the source it should
// be extracted from
type Argument struct {
	Source       string `json:"source,omitempty"`
	Name         string `json:"name,omitempty"`
	EnvName      string `json:"envname,omitempty"`
	Base64Decode bool   `json:"base64decode,omitempty"`
}

// Get Argument method returns the value for the Argument's key name
// based on the Argument's source.
// Wraps the error in an ArgumentError if any.
func (ha *Argument) Get(r *Request) (string, error) {
	value, err := ha.get(r)
	if err != nil {
		return value, &ArgumentError{*ha, err}
	}
	return value, nil
}

// Get Argument method returns the value for the Argument's key name
// based on the Argument's source
func (ha *Argument) get(r *Request) (string, error) {
	var source *map[string]interface{}
	key := ha.Name

	switch ha.Source {
	case SourceHeader:
		source = &r.Headers
		key = textproto.CanonicalMIMEHeaderKey(ha.Name)

	case SourceQuery, SourceQueryAlias:
		source = &r.Query

	case SourcePayload:
		source = &r.Payload

	case SourceString:
		return ha.Name, nil

	case SourceRawRequestBody:
		return string(r.Body), nil

	case SourceRequest:
		if r == nil || r.RawRequest == nil {
			return "", errors.New("request is nil")
		}

		switch strings.ToLower(ha.Name) {
		case "remote-addr":
			return r.RawRequest.RemoteAddr, nil
		case "method":
			return r.RawRequest.Method, nil
		default:
			return "", fmt.Errorf("unsupported request key: %q", ha.Name)
		}

	case SourceEntirePayload:
		res, err := json.Marshal(&r.Payload)
		if err != nil {
			return "", fmt.Errorf("JSON encode failed: %w", err)
		}

		return string(res), nil

	case SourceEntireHeaders:
		res, err := json.Marshal(&r.Headers)
		if err != nil {
			return "", fmt.Errorf("JSON encode failed: %w", err)
		}

		return string(res), nil

	case SourceEntireQuery:
		res, err := json.Marshal(&r.Query)
		if err != nil {
			return "", err
		}

		return string(res), nil
	}

	if source != nil {
		return ExtractParameterAsString(key, *source)
	}

	return "", errors.New("no source for value retrieval")
}
