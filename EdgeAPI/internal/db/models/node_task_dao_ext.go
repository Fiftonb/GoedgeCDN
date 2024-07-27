// Copyright 2022 GoEdge CDN goedge.cdn@gmail.com. All rights reserved. Official site: https://goedge.cloud .
//go:build !plus

package models

import "github.com/iwind/TeaGo/dbs"

// ExtractNSClusterTask 分解NS节点集群任务
func (this *NodeTaskDAO) ExtractNSClusterTask(tx *dbs.Tx, clusterId int64, taskType NodeTaskType) error {
	return nil
}
