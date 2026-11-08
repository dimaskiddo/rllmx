package tests

import (
	"context"
	"fmt"
	"sync"

	"github.com/goravel/framework/contracts/log"
)

// Log is a log.Log that keeps what the Info, Warning and Error calls write.
// Any other method panics, which is how a test notices a call it did not expect.
type Log struct {
	log.Log
	mu    sync.Mutex
	lines []string
}

func (l *Log) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, s)
}

// Lines returns a copy of everything logged so far.
func (l *Log) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *Log) Info(args ...any)               { l.add(fmt.Sprint(args...)) }
func (l *Log) Infof(f string, args ...any)    { l.add(fmt.Sprintf(f, args...)) }
func (l *Log) Warning(args ...any)            { l.add(fmt.Sprint(args...)) }
func (l *Log) Warningf(f string, args ...any) { l.add(fmt.Sprintf(f, args...)) }
func (l *Log) Error(args ...any)              { l.add(fmt.Sprint(args...)) }
func (l *Log) Errorf(f string, args ...any)   { l.add(fmt.Sprintf(f, args...)) }
func (l *Log) Debugf(f string, args ...any)   { l.add(fmt.Sprintf(f, args...)) }
func (l *Log) Panicf(f string, args ...any)   { panic(fmt.Sprintf(f, args...)) }

// WithContext returns the same Log: the database logger calls it before every
// write and the lines still land in one place.
func (l *Log) WithContext(context.Context) log.Log { return l }
