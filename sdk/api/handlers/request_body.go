package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

// ErrRequestBodyTooLarge reports that either the encoded request body or one
// of its decoded representations exceeded the configured limit.
var ErrRequestBodyTooLarge = errors.New("request body too large")

const (
	// DefaultJSONRequestBodyMaxBytes bounds OpenAI-compatible JSON endpoints.
	// Native Claude endpoints use their tighter protocol-specific limit.
	DefaultJSONRequestBodyMaxBytes  int64 = 64 << 20
	maxRequestContentEncodingLayers       = 4
)

// ReadRequestBody reads the incoming request body and decodes supported
// Content-Encoding values before handlers inspect JSON fields.
func ReadRequestBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil, fmt.Errorf("request body is unavailable")
	}
	if c.Request.ContentLength > DefaultJSONRequestBodyMaxBytes {
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrRequestBodyTooLarge, DefaultJSONRequestBodyMaxBytes)
	}
	raw, err := readBodyAtMost(c.Request.Body, DefaultJSONRequestBodyMaxBytes)
	if err != nil {
		return nil, err
	}

	encoding := strings.TrimSpace(strings.Join(c.Request.Header.Values("Content-Encoding"), ","))
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	decoded, err := decodeRequestBodyLimited(raw, encoding, DefaultJSONRequestBodyMaxBytes)
	if err != nil {
		if json.Valid(raw) {
			return raw, nil
		}
		return nil, err
	}
	return decoded, nil
}

// RequestBodyErrorStatus maps a body-size rejection to 413 while preserving
// 400 for malformed or unsupported request encodings.
func RequestBodyErrorStatus(err error) int {
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// ReadStrictJSONRequestBody reads at most maxBytes from the request, decodes
// supported content encodings with the same limit, and rejects malformed JSON
// or duplicate object keys at any nesting depth.
func ReadStrictJSONRequestBody(c *gin.Context, maxBytes int64) ([]byte, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil, fmt.Errorf("request body is unavailable")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("request body limit must be positive")
	}
	if c.Request.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrRequestBodyTooLarge, maxBytes)
	}

	raw, err := readBodyAtMost(c.Request.Body, maxBytes)
	if err != nil {
		return nil, err
	}

	encoding := strings.TrimSpace(strings.Join(c.Request.Header.Values("Content-Encoding"), ","))
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		raw, err = decodeRequestBodyLimited(raw, encoding, maxBytes)
		if err != nil {
			return nil, err
		}
	}

	if err = validateJSONWithoutDuplicateKeys(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func readBodyAtMost(reader io.Reader, maxBytes int64) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, fmt.Errorf("%w: limit is %d bytes", ErrRequestBodyTooLarge, maxBytes)
		}
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrRequestBodyTooLarge, maxBytes)
	}
	return body, nil
}

func decodeRequestBodyLimited(raw []byte, encoding string, maxBytes int64) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	if len(parts) > maxRequestContentEncodingLayers {
		return nil, fmt.Errorf("too many request content encodings: maximum is %d", maxRequestContentEncodingLayers)
	}
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "identity":
			continue
		case "zstd":
			decoded, err := decodeZstdRequestBodyLimited(body, maxBytes)
			if err != nil {
				return nil, err
			}
			body = decoded
		case "":
			return nil, fmt.Errorf("invalid empty request content encoding")
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeZstdRequestBodyLimited(raw []byte, maxBytes int64) ([]byte, error) {
	decoder, err := zstd.NewReader(
		bytes.NewReader(raw),
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(uint64(maxBytes)),
	)
	if err != nil {
		if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
			return nil, fmt.Errorf("%w: limit is %d bytes", ErrRequestBodyTooLarge, maxBytes)
		}
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	decoded, err := readBodyAtMost(decoder, maxBytes)
	if err != nil {
		if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
			return nil, fmt.Errorf("%w: limit is %d bytes", ErrRequestBodyTooLarge, maxBytes)
		}
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	return decoded, nil
}

func validateJSONWithoutDuplicateKeys(raw []byte) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid JSON")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		return fmt.Errorf("invalid JSON: multiple top-level values")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	delim, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delim {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, errKey := decoder.Token()
			if errKey != nil {
				return fmt.Errorf("invalid JSON object key: %w", errKey)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate JSON object key")
			}
			keys[key] = struct{}{}
			if errValue := validateJSONValue(decoder); errValue != nil {
				return errValue
			}
		}
		end, errEnd := decoder.Token()
		if errEnd != nil || end != json.Delim('}') {
			if errEnd != nil {
				return fmt.Errorf("invalid JSON object: %w", errEnd)
			}
			return fmt.Errorf("invalid JSON object")
		}
		return nil
	case '[':
		for decoder.More() {
			if errValue := validateJSONValue(decoder); errValue != nil {
				return errValue
			}
		}
		end, errEnd := decoder.Token()
		if errEnd != nil || end != json.Delim(']') {
			if errEnd != nil {
				return fmt.Errorf("invalid JSON array: %w", errEnd)
			}
			return fmt.Errorf("invalid JSON array")
		}
		return nil
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
}
