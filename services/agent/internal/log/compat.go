// Package log provides a logrus-compatible API on top of go-common/logger (zap).
// This avoids rewriting hundreds of logger.WithField().Info() calls across the
// service while still using zap as the backend. New code should use go-common/logger directly.
package log

import (
	gocommonlogger "github.com/decisionbox-io/decisionbox/libs/go-common/logger"
	"go.uber.org/zap"
)

// Fields is a map of key-value pairs for structured logging.
type Fields map[string]interface{}

var log *gocommonlogger.Logger

func ensureInit() {
	if log == nil {
		log = gocommonlogger.New("default", "warn")
	}
}

// Init initializes the global logger.
func Init(service, level string) {
	log = gocommonlogger.New(service, level)
}

// Sync flushes the logger.
func Sync() { _ = log.Sync() }

// entry wraps pending fields for chained logging.
type entry struct {
	fields []zap.Field
}

// WithField creates a log entry with a single field.
func WithField(key string, value interface{}) *entry {
	ensureInit()
	return &entry{fields: []zap.Field{zap.Any(key, value)}}
}

// WithFields creates a log entry with multiple fields.
func WithFields(fields Fields) *entry {
	ensureInit()
	e := &entry{}
	for k, v := range fields {
		e.fields = append(e.fields, zap.Any(k, v))
	}
	return e
}

// WithError creates a log entry with an error field.
func WithError(err error) *entry {
	ensureInit()
	return &entry{fields: []zap.Field{zap.Error(err)}}
}

func (e *entry) WithField(key string, value interface{}) *entry {
	e.fields = append(e.fields, zap.Any(key, value))
	return e
}

func (e *entry) WithFields(fields Fields) *entry {
	for k, v := range fields {
		e.fields = append(e.fields, zap.Any(k, v))
	}
	return e
}

func (e *entry) WithError(err error) *entry {
	e.fields = append(e.fields, zap.Error(err))
	return e
}

func (e *entry) Debug(msg string) { ensureInit(); log.Debug(msg, e.fields...) }
func (e *entry) Info(msg string)  { ensureInit(); log.Info(msg, e.fields...) }
func (e *entry) Warn(msg string)  { ensureInit(); log.Warn(msg, e.fields...) }
func (e *entry) Error(msg string) { ensureInit(); log.Error(msg, e.fields...) }
func (e *entry) Fatal(msg string) { ensureInit(); log.Fatal(msg, e.fields...) }

// Package-level convenience methods.
func Debug(msg string) { ensureInit(); log.Debug(msg) }
func Info(msg string)  { ensureInit(); log.Info(msg) }
func Warn(msg string)  { ensureInit(); log.Warn(msg) }
func Error(msg string) { ensureInit(); log.Error(msg) }
func Fatal(msg string) { ensureInit(); log.Fatal(msg) }
