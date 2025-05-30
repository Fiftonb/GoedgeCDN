// Copyright 2023 GoEdge CDN goedge.cdn@gmail.com. All rights reserved. Official site: https://goedge.cloud .

package taskutils

import (
	"errors"
	"reflect"
	"sync"
)

const DefaultConcurrent = 16

func RunConcurrent(tasks any, concurrent int, f func(task any, locker *sync.RWMutex)) error {
	if tasks == nil {
		return nil
	}
	var tasksValue = reflect.ValueOf(tasks)
	if tasksValue.Type().Kind() != reflect.Slice {
		return errors.New("ony works for slice")
	}

	var countTasks = tasksValue.Len()
	if countTasks == 0 {
		return nil
	}

	if concurrent <= 0 {
		concurrent = 8
	}
	if concurrent > countTasks {
		concurrent = countTasks
	}

	var taskChan = make(chan any, countTasks)
	for i := 0; i < countTasks; i++ {
		taskChan <- tasksValue.Index(i).Interface()
	}

	var wg = &sync.WaitGroup{}
	wg.Add(concurrent)

	var locker = &sync.RWMutex{}

	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()

			for {
				select {
				case task := <-taskChan:
					f(task, locker)
				default:
					return
				}
			}
		}()
	}
	wg.Wait()

	return nil
}
