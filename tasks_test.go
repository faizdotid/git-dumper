package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessTasksDeduplicates(t *testing.T) {
	var mu sync.Mutex
	processed := map[string]int{}

	processTasks([]string{"a", "b", "a"}, nil, 4, func(task string) []string {
		mu.Lock()
		processed[task]++
		mu.Unlock()

		switch task {
		case "a":
			return []string{"c"}
		case "b":
			return []string{"c", "a"} // "a" was already seen
		}
		return nil
	})

	mu.Lock()
	defer mu.Unlock()
	for _, task := range []string{"a", "b", "c"} {
		if processed[task] != 1 {
			t.Errorf("task %q processed %d times, want 1", task, processed[task])
		}
	}
	if len(processed) != 3 {
		t.Errorf("unexpected tasks processed: %v", processed)
	}
}

func TestProcessTasksSkipsDone(t *testing.T) {
	var mu sync.Mutex
	var processed []string

	processTasks([]string{"done", "new"}, []string{"done"}, 2, func(task string) []string {
		mu.Lock()
		processed = append(processed, task)
		mu.Unlock()
		return nil
	})

	if len(processed) != 1 || processed[0] != "new" {
		t.Errorf("processed = %v, want [new]", processed)
	}
}

func TestProcessTasksRecoversPanics(t *testing.T) {
	var mu sync.Mutex
	processed := map[string]bool{}

	processTasks([]string{"boom", "ok"}, nil, 2, func(task string) []string {
		mu.Lock()
		processed[task] = true
		mu.Unlock()
		if task == "boom" {
			panic("kaboom")
		}
		return nil
	})

	if !processed["boom"] || !processed["ok"] {
		t.Errorf("processed = %v, want both tasks", processed)
	}
}

func TestProcessTasksEmpty(t *testing.T) {
	called := false
	processTasks(nil, nil, 2, func(string) []string {
		called = true
		return nil
	})
	if called {
		t.Error("task function called with no initial tasks")
	}
}

func TestProcessTasksWorkerBound(t *testing.T) {
	const jobs = 3
	tasks := make([]string, 50)
	for i := range tasks {
		tasks[i] = fmt.Sprintf("task-%d", i)
	}

	var running, maxRunning atomic.Int32
	processTasks(tasks, nil, jobs, func(string) []string {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		running.Add(-1)
		return nil
	})

	if got := maxRunning.Load(); got > jobs {
		t.Errorf("max concurrent workers = %d, want <= %d", got, jobs)
	}
}
