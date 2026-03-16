// Package log provides structured logging for the DecisionBox API.
// Wraps go-common/logger with convenience methods matching the agent's pattern.
package log

import (
	gocommonlogger "github.com/decisionbox-io/decisionbox/libs/go-common/logger"
	"go.uber.org/zap"
)

type Fields map[string]interface{}

var logger *gocommonlogger.Logger

func init() {
	logger = gocommonlogger.New("decisionbox-api", "info")
}

// Init initializes the logger with a specific level.
func Init(level string) {
	logger = gocommonlogger.New("decisionbox-api", level)
}

func Sync() { _ = logger.Sync() }

type entry struct {
	fields []zap.Field
}

func WithField(key string, value interface{}) *entry {
	return &entry{fields: []zap.Field{zap.Any(key, value)}}
}

func WithFields(fields Fields) *entry {
	e := &entry{}
	for k, v := range fields {
		e.fields = append(e.fields, zap.Any(k, v))
	}
	return e
}

func WithError(err error) *entry {
	return &entry{fields: []zap.Field{zap.Error(err)}}
}

func (e *entry) Debug(msg string) { logger.Debug(msg, e.fields...) }
func (e *entry) Info(msg string)  { logger.Info(msg, e.fields...) }
func (e *entry) Warn(msg string)  { logger.Warn(msg, e.fields...) }
func (e *entry) Error(msg string) { logger.Error(msg, e.fields...) }

func Debug(msg string) { logger.Debug(msg) }
func Info(msg string)  { logger.Info(msg) }
func Warn(msg string)  { logger.Warn(msg) }
func Error(msg string) { logger.Error(msg) }
