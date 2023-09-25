package hook

import (
	"crypto/subtle"
	"log/slog"
	"regexp"
)

// Rules is a structure that contains one of the valid rule types
type Rules struct {
	And   *AndRule   `json:"and,omitempty"`
	Or    *OrRule    `json:"or,omitempty"`
	Not   *NotRule   `json:"not,omitempty"`
	Match *MatchRule `json:"match,omitempty"`
}

// Evaluate finds the first rule property that is not nil and returns the value
// it evaluates to
func (r Rules) Evaluate(req *Request) (bool, error) {
	switch {
	case r.And != nil:
		return r.And.Evaluate(req)
	case r.Or != nil:
		return r.Or.Evaluate(req)
	case r.Not != nil:
		return r.Not.Evaluate(req)
	case r.Match != nil:
		return r.Match.Evaluate(req)
	}

	return false, nil
}

// AndRule will evaluate to true if and only if all of the ChildRules evaluate to true
type AndRule []Rules

// Evaluate AndRule will return true if and only if all of ChildRules evaluate to true
func (r AndRule) Evaluate(req *Request) (bool, error) {
	res := true

	for _, v := range r {
		rv, err := v.Evaluate(req)
		if err != nil {
			return false, err
		}

		res = res && rv
		if !res {
			return res, nil
		}
	}

	return res, nil
}

// OrRule will evaluate to true if any of the ChildRules evaluate to true
type OrRule []Rules

// Evaluate OrRule will return true if any of ChildRules evaluate to true
func (r OrRule) Evaluate(req *Request) (bool, error) {
	res := false

	for _, v := range r {
		rv, err := v.Evaluate(req)
		if err != nil {
			if !IsParameterNodeError(err) {
				if !req.AllowSignatureErrors || (req.AllowSignatureErrors && !IsSignatureError(err)) {
					return false, err
				}
			}
		}

		res = res || rv
		if res {
			return res, nil
		}
	}

	return res, nil
}

// NotRule will evaluate to true if any and only if the ChildRule evaluates to false
type NotRule Rules

// Evaluate NotRule will return true if and only if ChildRule evaluates to false
func (r NotRule) Evaluate(req *Request) (bool, error) {
	rv, err := Rules(r).Evaluate(req)
	return !rv, err
}

// MatchRule will evaluate to true based on the type
type MatchRule struct {
	Type      string   `json:"type,omitempty"`
	Regex     string   `json:"regex,omitempty"`
	Secret    string   `json:"secret,omitempty"`
	Value     string   `json:"value,omitempty"`
	Parameter Argument `json:"parameter,omitempty"`
	IPRange   string   `json:"ip-range,omitempty"`
}

// Constants for the MatchRule type
const (
	MatchValue      string = "value"
	MatchRegex      string = "regex"
	MatchHMACSHA1   string = "payload-hmac-sha1"
	MatchHMACSHA256 string = "payload-hmac-sha256"
	MatchHMACSHA512 string = "payload-hmac-sha512"
	MatchHashSHA1   string = "payload-hash-sha1"
	MatchHashSHA256 string = "payload-hash-sha256"
	MatchHashSHA512 string = "payload-hash-sha512"
	IPWhitelist     string = "ip-whitelist"
	ScalrSignature  string = "scalr-signature"
)

// Evaluate MatchRule will return based on the type
func (r MatchRule) Evaluate(req *Request) (bool, error) {
	if r.Type == IPWhitelist {
		return CheckIPWhitelist(req.RawRequest.RemoteAddr, r.IPRange)
	}
	if r.Type == ScalrSignature {
		return CheckScalrSignature(req, r.Secret, true)
	}

	arg, err := r.Parameter.Get(req)
	if err == nil {
		switch r.Type {
		case MatchValue:
			return compare(arg, r.Value), nil
		case MatchRegex:
			return regexp.MatchString(r.Regex, arg)
		case MatchHashSHA1:
			slog.Warn("use of deprecated option payload-hash-sha1; use payload-hmac-sha1 instead")
			fallthrough
		case MatchHMACSHA1:
			_, err := CheckPayloadSignature(req.Body, r.Secret, arg)
			return err == nil, err
		case MatchHashSHA256:
			slog.Warn("use of deprecated option payload-hash-sha256; use payload-hmac-sha256 instead")
			fallthrough
		case MatchHMACSHA256:
			_, err := CheckPayloadSignature256(req.Body, r.Secret, arg)
			return err == nil, err
		case MatchHashSHA512:
			slog.Warn("use of deprecated option payload-hash-sha512; use payload-hmac-sha512 instead")
			fallthrough
		case MatchHMACSHA512:
			_, err := CheckPayloadSignature512(req.Body, r.Secret, arg)
			return err == nil, err
		}
	}
	return false, err
}

// compare is a helper function for constant time string comparisons.
func compare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
