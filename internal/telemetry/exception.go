// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
)

// exceptionTyped is the handler of the logger. It gives a record with an error the attribute
// exception.type, with the type of the error under the wrappers that say nothing of the cause.
//
// The log SDK derives exception.type from the error itself when a record has none, and it looks
// through the wrappers of fmt.Errorf only. A failure that another try can pass carries the mark
// of internal/retry, so every such failure would name the same type, the mark. The SDK keeps an
// exception.type that the record already has.
//
// The handler also takes the end of the context away from each record. A signal cancels the
// context of the process, and the work that runs when it comes still logs with that context. The
// log SDK drops a record whose context has ended, so the last records of a process, which are the
// interesting ones, would be lost. The record keeps the values of the context, so its trace stays.
type exceptionTyped struct {
	slog.Handler
}

func (h exceptionTyped) Handle(ctx context.Context, record slog.Record) error {
	ctx = context.WithoutCancel(ctx)

	var failure error

	record.Attrs(func(attr slog.Attr) bool {
		if attr.Value.Kind() != slog.KindAny {
			return true
		}

		if err, ok := attr.Value.Any().(error); ok {
			failure = err
		}

		return true
	})

	if failure != nil {
		record = record.Clone()
		record.AddAttrs(slog.String(exceptionTypeKey, exceptionType(failure)))
	}

	return h.Handler.Handle(ctx, record)
}

func (h exceptionTyped) WithAttrs(attrs []slog.Attr) slog.Handler {
	return exceptionTyped{h.Handler.WithAttrs(attrs)}
}

func (h exceptionTyped) WithGroup(name string) slog.Handler {
	return exceptionTyped{h.Handler.WithGroup(name)}
}

// exceptionTypeKey is the key of the attribute of the conventions that names the type of an error.
const exceptionTypeKey = string(semconv.ExceptionTypeKey)

// wrapped is the type of an error that fmt.Errorf gives with one %w.
var wrapped = reflect.TypeOf(fmt.Errorf("wrapped: %w", errors.New("cause")))

// exceptionType gives the type of the cause of an error: the error under each wrapper of
// fmt.Errorf and each mark of internal/retry. It names a type as the log SDK does, so that a name
// from here and a name from the SDK agree.
func exceptionType(err error) string {
	for {
		if cause := retry.Unmark(err); cause != nil {
			err = cause

			continue
		}

		if reflect.TypeOf(err) == wrapped {
			if cause := errors.Unwrap(err); cause != nil {
				err = cause

				continue
			}
		}

		break
	}

	if typed, ok := err.(interface{ ErrorType() string }); ok && typed.ErrorType() != "" {
		return typed.ErrorType()
	}

	of := reflect.TypeOf(err)
	if of.PkgPath() != "" && of.Name() != "" {
		return of.PkgPath() + "." + of.Name()
	}

	return of.String()
}
