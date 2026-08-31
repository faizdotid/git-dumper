package main

import (
	"sync"
)

// TaskFunc processes a task and returns a list of follow-up tasks.
type TaskFunc func(task string) []string

// processTasks processes tasks in parallel, mirroring the Python
// process_tasks(): a pool of workers pulls tasks, each task may yield new
// tasks, and duplicates (including tasks already done) are skipped.
func processTasks(initialTasks, tasksDone []string, jobs int, fn TaskFunc) {
	if len(initialTasks) == 0 {
		return
	}

	seen := make(map[string]bool, len(tasksDone)+len(initialTasks))
	for _, task := range tasksDone {
		seen[task] = true
	}

	var queue []string
	for _, task := range initialTasks {
		if task != "" && !seen[task] {
			seen[task] = true
			queue = append(queue, task)
		}
	}

	pending := make(chan string)
	results := make(chan []string)

	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range pending {
				results <- doTaskSafe(fn, task)
			}
		}()
	}

	inFlight := 0
	for inFlight > 0 || len(queue) > 0 {
		var next string
		var pendingCh chan string
		if len(queue) > 0 {
			next = queue[0]
			pendingCh = pending
		}

		select {
		case pendingCh <- next:
			queue = queue[1:]
			inFlight++
		case result := <-results:
			inFlight--
			for _, task := range result {
				if task != "" && !seen[task] {
					seen[task] = true
					queue = append(queue, task)
				}
			}
		}
	}

	close(pending)
	wg.Wait()
}

// doTaskSafe mirrors the Python Worker: a task that panics is logged and
// produces no follow-up tasks instead of aborting the whole dump.
func doTaskSafe(fn TaskFunc, task string) (result []string) {
	defer func() {
		if r := recover(); r != nil {
			eprintf("[-] Task %s failed unexpectedly: %v\n", task, r)
			result = nil
		}
	}()
	return fn(task)
}
