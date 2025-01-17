package wpool

import (
	"context"
	"log/slog"
)

type disabledSlogHandler struct{}

func (d disabledSlogHandler) Enabled(_ context.Context, _ slog.Level) bool  { return false }
func (d disabledSlogHandler) Handle(_ context.Context, _ slog.Record) error { return nil }
func (d disabledSlogHandler) WithAttrs(_ []slog.Attr) slog.Handler          { return disabledSlogHandler{} }
func (d disabledSlogHandler) WithGroup(_ string) slog.Handler               { return disabledSlogHandler{} }
