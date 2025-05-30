package models

import (
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/iwind/TeaGo/dbs"
)

func TestHTTPHeaderPolicyDAO_FindHeaderPolicyIdWithHeaderId(t *testing.T) {
	dbs.NotifyReady()
	var tx *dbs.Tx
	policyId, err := SharedHTTPHeaderPolicyDAO.FindHeaderPolicyIdWithHeaderId(tx, 15)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("policyId:", policyId)
}
