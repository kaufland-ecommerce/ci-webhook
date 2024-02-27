package hook

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/textproto"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"
)

// Constants used to specify the parameter source
const (
	SourceHeader         string = "header"
	SourceQuery          string = "url"
	SourceQueryAlias     string = "query"
	SourcePayload        string = "payload"
	SourceRawRequestBody string = "raw-request-body"
	SourceRequest        string = "request"
	SourceString         string = "string"
	SourceEntirePayload  string = "entire-payload"
	SourceEntireQuery    string = "entire-query"
	SourceEntireHeaders  string = "entire-headers"
)

const (
	// EnvNamespace is the prefix used for passing arguments into the command
	// environment.
	EnvNamespace string = "HOOK_"
)

// ParameterNodeError describes an error walking a parameter node.
type ParameterNodeError struct {
	key string
}

func (e *ParameterNodeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("parameter node not found: %s", e.key)
}

// IsParameterNodeError returns whether err is of type ParameterNodeError.
func IsParameterNodeError(err error) bool {
	var e = &ParameterNodeError{}
	if errors.As(err, &e) {
		return true
	}
	return false
}

// SourceError describes an invalid source passed to Hook.
type SourceError struct {
	Argument Argument
}

func (e *SourceError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("invalid source for argument %+v", e.Argument)
}

// ParseError describes an error parsing user input.
type ParseError struct {
	Err error
}

func (e *ParseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Err.Error()
}

// ExtractCommaSeparatedValues will extract the values matching the key.
func ExtractCommaSeparatedValues(source, prefix string) []string {
	parts := strings.Split(source, ",")
	values := make([]string, 0)
	for _, part := range parts {
		if strings.HasPrefix(part, prefix) {
			values = append(values, strings.TrimPrefix(part, prefix))
		}
	}

	return values
}

// ExtractSignatures will extract all the signatures from the source.
func ExtractSignatures(source, prefix string) []string {
	// If there are multiple possible matches, let the comma seperated extractor
	// do it's work.
	if strings.Contains(source, ",") {
		return ExtractCommaSeparatedValues(source, prefix)
	}

	// There were no commas, so just trim the prefix (if it even exists) and
	// pass it back.
	return []string{
		strings.TrimPrefix(source, prefix),
	}
}

// CheckIPWhitelist makes sure the provided remote address (of the form IP:port) falls within the provided IP range
// (in CIDR form or a single IP address).
func CheckIPWhitelist(remoteAddr, ipRange string) (bool, error) {
	// Extract IP address from remote address.

	// IPv6 addresses will likely be surrounded by [].
	ip := strings.Trim(remoteAddr, " []")

	if i := strings.LastIndex(ip, ":"); i != -1 {
		ip = ip[:i]
		ip = strings.Trim(ip, " []")
	}

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false, fmt.Errorf("invalid IP address found in remote address '%s'", remoteAddr)
	}

	for _, r := range strings.Fields(ipRange) {
		// Extract IP range in CIDR form.  If a single IP address is provided, turn it into CIDR form.

		if !strings.Contains(r, "/") {
			r = r + "/32"
		}

		_, cidr, err := net.ParseCIDR(r)
		if err != nil {
			return false, err
		}

		if cidr.Contains(parsedIP) {
			return true, nil
		}
	}

	return false, nil
}

// ReplaceParameter replaces parameter value with the passed value in the passed map
// (please note you should pass pointer to the map, because we're modifying it)
// based on the passed string
func ReplaceParameter(s string, params, value interface{}) bool {
	if params == nil {
		return false
	}

	if paramsValue := reflect.ValueOf(params); paramsValue.Kind() == reflect.Slice {
		if paramsValueSliceLength := paramsValue.Len(); paramsValueSliceLength > 0 {
			if p := strings.SplitN(s, ".", 2); len(p) > 1 {
				index, err := strconv.ParseUint(p[0], 10, 64)

				if err != nil || paramsValueSliceLength <= int(index) {
					return false
				}

				return ReplaceParameter(p[1], params.([]interface{})[index], value)
			}
		}

		return false
	}

	if p := strings.SplitN(s, ".", 2); len(p) > 1 {
		if pValue, ok := params.(map[string]interface{})[p[0]]; ok {
			return ReplaceParameter(p[1], pValue, value)
		}
	} else {
		if _, ok := (*params.(*map[string]interface{}))[p[0]]; ok {
			(*params.(*map[string]interface{}))[p[0]] = value
			return true
		}
	}

	return false
}

// GetParameter extracts interface{} value based on the passed string
func GetParameter(s string, params interface{}) (interface{}, error) {
	if params == nil {
		return nil, errors.New("no parameters")
	}

	paramsValue := reflect.ValueOf(params)

	switch paramsValue.Kind() {
	case reflect.Slice:
		paramsValueSliceLength := paramsValue.Len()
		if paramsValueSliceLength > 0 {

			if p := strings.SplitN(s, ".", 2); len(p) > 1 {
				index, err := strconv.ParseUint(p[0], 10, 64)

				if err != nil || paramsValueSliceLength <= int(index) {
					return nil, &ParameterNodeError{s}
				}

				return GetParameter(p[1], params.([]interface{})[index])
			}

			index, err := strconv.ParseUint(s, 10, 64)

			if err != nil || paramsValueSliceLength <= int(index) {
				return nil, &ParameterNodeError{s}
			}

			return params.([]interface{})[index], nil
		}

		return nil, &ParameterNodeError{s}

	case reflect.Map:
		// Check for raw key
		if v, ok := params.(map[string]interface{})[s]; ok {
			return v, nil
		}

		// Checked for dotted references
		p := strings.SplitN(s, ".", 2)
		if pValue, ok := params.(map[string]interface{})[p[0]]; ok {
			if len(p) > 1 {
				return GetParameter(p[1], pValue)
			}

			return pValue, nil
		}
	}

	return nil, &ParameterNodeError{s}
}

// ExtractParameterAsString extracts value from interface{} as string based on
// the passed string.  Complex data types are rendered as JSON instead of the Go
// Stringer format.
func ExtractParameterAsString(s string, params interface{}) (string, error) {
	pValue, err := GetParameter(s, params)
	if err != nil {
		return "", fmt.Errorf("parameter extraction failed: %w", err)
	}

	switch v := reflect.ValueOf(pValue); v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice:
		r, err := json.Marshal(pValue)
		if err != nil {
			return "", fmt.Errorf("JSON encode failed: %w", err)
		}

		return string(r), nil

	default:
		return fmt.Sprintf("%v", pValue), nil
	}
}

// Header is a structure containing header name and it's value
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ResponseHeaders is a slice of Header objects
type ResponseHeaders []Header

func (h *ResponseHeaders) String() string {
	// a 'hack' to display name=value in flag usage listing
	if len(*h) == 0 {
		return "name=value"
	}

	result := make([]string, len(*h))

	for idx, responseHeader := range *h {
		result[idx] = fmt.Sprintf("%s=%s", responseHeader.Name, responseHeader.Value)
	}

	return strings.Join(result, ", ")
}

// Set method appends new Header object from header=value notation
func (h *ResponseHeaders) Set(value string) error {
	splitResult := strings.SplitN(value, "=", 2)

	if len(splitResult) != 2 {
		return errors.New("header flag must be in name=value format")
	}

	*h = append(*h, Header{Name: splitResult[0], Value: splitResult[1]})
	return nil
}

// Hook type is a structure containing details for a single hook
type Hook struct {
	ID                                  string          `json:"id,omitempty"`
	ExecuteCommand                      string          `json:"execute-command,omitempty"`
	CommandWorkingDirectory             string          `json:"command-working-directory,omitempty"`
	ResponseMessage                     string          `json:"response-message,omitempty"`
	ResponseHeaders                     ResponseHeaders `json:"response-headers,omitempty"`
	CaptureCommandOutput                bool            `json:"include-command-output-in-response,omitempty"`
	StreamCommandOutput                 bool            `json:"stream-command-output,omitempty"`
	CaptureCommandOutputOnError         bool            `json:"include-command-output-in-response-on-error,omitempty"`
	PassEnvironmentToCommand            []Argument      `json:"pass-environment-to-command,omitempty"`
	PassArgumentsToCommand              []Argument      `json:"pass-arguments-to-command,omitempty"`
	PassFileToCommand                   []Argument      `json:"pass-file-to-command,omitempty"`
	JSONStringParameters                []Argument      `json:"parse-parameters-as-json,omitempty"`
	TriggerRule                         *Rules          `json:"trigger-rule,omitempty"`
	TriggerRuleMismatchHttpResponseCode int             `json:"trigger-rule-mismatch-http-response-code,omitempty"`
	TriggerSignatureSoftFailures        bool            `json:"trigger-signature-soft-failures,omitempty"`
	IncomingPayloadContentType          string          `json:"incoming-payload-content-type,omitempty"`
	SuccessHttpResponseCode             int             `json:"success-http-response-code,omitempty"`
	HTTPMethods                         []string        `json:"http-methods"`
	Timeout                             time.Duration   `json:"timeout,omitempty"`
}

// ParseJSONParameters decodes specified arguments to JSON objects and replaces the
// string with the newly created object
// todo: move to Request
func (h *Hook) ParseJSONParameters(r *Request) error {
	var result *multierror.Error

	for i := range h.JSONStringParameters {
		arg, err := h.JSONStringParameters[i].Get(r)
		if err != nil {
			result = multierror.Append(result, err)
		} else {
			var newArg map[string]interface{}

			decoder := json.NewDecoder(strings.NewReader(string(arg)))
			decoder.UseNumber()

			err := decoder.Decode(&newArg)
			if err != nil {
				result = multierror.Append(result, &ParseError{err})
				continue
			}

			var source *map[string]interface{}

			switch h.JSONStringParameters[i].Source {
			case SourceHeader:
				source = &r.Headers
			case SourcePayload:
				source = &r.Payload
			case SourceQuery, SourceQueryAlias:
				source = &r.Query
			}

			if source != nil {
				key := h.JSONStringParameters[i].Name

				if h.JSONStringParameters[i].Source == SourceHeader {
					key = textproto.CanonicalMIMEHeaderKey(h.JSONStringParameters[i].Name)
				}

				ReplaceParameter(key, source, newArg)
			} else {
				result = multierror.Append(result, &SourceError{h.JSONStringParameters[i]})
			}
		}
	}
	return result.ErrorOrNil()
}

// ExtractCommandArguments creates a list of arguments, based on the
// PassArgumentsToCommand property that is ready to be used with exec.Command()
func (h *Hook) ExtractCommandArguments(r *Request) ([]string, error) {
	args := make([]string, 0)
	var result *multierror.Error
	args = append(args, h.ExecuteCommand)

	for i := range h.PassArgumentsToCommand {
		arg, err := h.PassArgumentsToCommand[i].Get(r)
		if err != nil {
			args = append(args, "")
			result = multierror.Append(result, err)
			continue
		}
		args = append(args, arg)
	}

	return args, result.ErrorOrNil()
}

// ExtractCommandArgumentsForEnv creates a list of arguments in key=value
// format, based on the PassEnvironmentToCommand property that is ready to be used
// with exec.Command().
func (h *Hook) ExtractCommandArgumentsForEnv(r *Request) ([]string, error) {
	args := make([]string, 0)
	var result *multierror.Error
	for i := range h.PassEnvironmentToCommand {
		arg, err := h.PassEnvironmentToCommand[i].Get(r)
		if err != nil {
			result = multierror.Append(result, err)
			continue
		}

		if h.PassEnvironmentToCommand[i].EnvName != "" {
			// first try to use the EnvName if specified
			args = append(args, h.PassEnvironmentToCommand[i].EnvName+"="+arg)
		} else {
			// then fallback on the name
			args = append(args, EnvNamespace+h.PassEnvironmentToCommand[i].Name+"="+arg)
		}
	}

	return args, result.ErrorOrNil()
}

// FileParameter describes a pass-file-to-command instance to be stored as file
type FileParameter struct {
	File    *os.File
	EnvName string
	Data    []byte
}

// ExtractCommandArgumentsForFile creates a list of arguments in key=value
// format, based on the PassFileToCommand property that is ready to be used
// with exec.Command().
func (h *Hook) ExtractCommandArgumentsForFile(r *Request) ([]FileParameter, error) {
	args := make([]FileParameter, 0)
	var result *multierror.Error
	for i := range h.PassFileToCommand {
		arg, err := h.PassFileToCommand[i].Get(r)
		if err != nil {
			result = multierror.Append(result, &ArgumentError{h.PassFileToCommand[i], err})
			continue
		}

		if h.PassFileToCommand[i].EnvName == "" {
			// if no environment-variable name is set, fall-back on the name
			slog.Debug("no ENVVAR name specified, using fallback",
				"fallback", EnvNamespace+strings.ToUpper(h.PassFileToCommand[i].Name))
			h.PassFileToCommand[i].EnvName = EnvNamespace + strings.ToUpper(h.PassFileToCommand[i].Name)
		}

		var fileContent []byte
		if h.PassFileToCommand[i].Base64Decode {
			dec, err := base64.StdEncoding.DecodeString(arg)
			if err != nil {
				slog.Error("error decoding base64 while extracting argument to file",
					"argument_name", h.PassFileToCommand[i].Name,
					"error", err)
			}
			fileContent = []byte(dec)
		} else {
			fileContent = []byte(arg)
		}

		args = append(args, FileParameter{EnvName: h.PassFileToCommand[i].EnvName, Data: fileContent})
	}

	return args, result.ErrorOrNil()
}
