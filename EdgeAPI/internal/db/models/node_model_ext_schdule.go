// Copyright 2023 GoEdge CDN goedge.cdn@gmail.com. All rights reserved. Official site: https://goedge.cloud .
//go:build !plus

package models

// HasScheduleSettings 检查是否设置了调度
func (this *Node) HasScheduleSettings() bool {
	return false
}
