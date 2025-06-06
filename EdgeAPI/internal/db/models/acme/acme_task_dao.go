package acme

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/TeaOSLab/EdgeCommon/pkg/serverconfigs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	acmeutils "github.com/TeaOSLab/EdgeAPI/internal/acme"
	teaconst "github.com/TeaOSLab/EdgeAPI/internal/const"
	"github.com/TeaOSLab/EdgeAPI/internal/db/models"
	"github.com/TeaOSLab/EdgeAPI/internal/db/models/dns"
	dbutils "github.com/TeaOSLab/EdgeAPI/internal/db/utils"
	"github.com/TeaOSLab/EdgeAPI/internal/dnsclients"
	"github.com/TeaOSLab/EdgeAPI/internal/errors"
	"github.com/TeaOSLab/EdgeAPI/internal/remotelogs"
	"github.com/TeaOSLab/EdgeAPI/internal/utils"
	"github.com/TeaOSLab/EdgeCommon/pkg/serverconfigs/sslconfigs"
	"github.com/go-acme/lego/v4/registration"
	_ "github.com/go-sql-driver/mysql"
	"github.com/iwind/TeaGo/Tea"
	"github.com/iwind/TeaGo/dbs"
	"github.com/iwind/TeaGo/logs"
	"github.com/iwind/TeaGo/maps"
	"github.com/iwind/TeaGo/types"
)

const (
	ACMETaskStateEnabled  = 1 // 已启用
	ACMETaskStateDisabled = 0 // 已禁用

	ACMETaskStatusPending     = 0
	ACMETaskStatusDone        = 1
	ACMETaskStatusRunning     = 2
	ACMETaskStatusIssueFailed = 3
)

var runningTaskMap sync.Map
var serverBindMutex = &sync.Mutex{}

type ACMETaskDAO dbs.DAO

func NewACMETaskDAO() *ACMETaskDAO {
	return dbs.NewDAO(&ACMETaskDAO{
		DAOObject: dbs.DAOObject{
			DB:     Tea.Env,
			Table:  "edgeACMETasks",
			Model:  new(ACMETask),
			PkName: "id",
		},
	}).(*ACMETaskDAO)
}

var SharedACMETaskDAO *ACMETaskDAO

func init() {
	dbs.OnReady(func() {
		SharedACMETaskDAO = NewACMETaskDAO()
	})
}

// EnableACMETask 启用条目
func (this *ACMETaskDAO) EnableACMETask(tx *dbs.Tx, id int64) error {
	_, err := this.Query(tx).
		Pk(id).
		Set("state", ACMETaskStateEnabled).
		Update()
	return err
}

// DisableACMETask 禁用条目
func (this *ACMETaskDAO) DisableACMETask(tx *dbs.Tx, id int64) error {
	_, err := this.Query(tx).
		Pk(id).
		Set("state", ACMETaskStateDisabled).
		Update()
	return err
}

// FindEnabledACMETask 查找启用中的条目
func (this *ACMETaskDAO) FindEnabledACMETask(tx *dbs.Tx, id int64) (*ACMETask, error) {
	result, err := this.Query(tx).
		Pk(id).
		Attr("state", ACMETaskStateEnabled).
		Find()
	if result == nil {
		return nil, err
	}
	return result.(*ACMETask), err
}

// CountACMETasksWithACMEUserId 计算某个ACME用户相关的任务数量
func (this *ACMETaskDAO) CountACMETasksWithACMEUserId(tx *dbs.Tx, acmeUserId int64) (int64, error) {
	return this.Query(tx).
		State(ACMETaskStateEnabled).
		Attr("acmeUserId", acmeUserId).
		Count()
}

// CountACMETasksWithDNSProviderId 计算某个DNS服务商相关的任务数量
func (this *ACMETaskDAO) CountACMETasksWithDNSProviderId(tx *dbs.Tx, dnsProviderId int64) (int64, error) {
	return this.Query(tx).
		State(ACMETaskStateEnabled).
		Attr("dnsProviderId", dnsProviderId).
		Count()
}

// DisableAllTasksWithCertId 停止某个证书相关任务
func (this *ACMETaskDAO) DisableAllTasksWithCertId(tx *dbs.Tx, certId int64) error {
	_, err := this.Query(tx).
		Attr("certId", certId).
		Set("state", ACMETaskStateDisabled).
		Update()
	return err
}

// CountAllEnabledACMETasks 计算所有任务数量
func (this *ACMETaskDAO) CountAllEnabledACMETasks(tx *dbs.Tx, userId int64, isAvailable bool, isExpired bool, expiringDays int64, keyword string, userOnly bool) (int64, error) {
	var query = this.Query(tx)
	if userId > 0 {
		query.Attr("userId", userId)
	} else {
		if userOnly {
			query.Gt("userId", 0)
		} else {
			query.Attr("userId", 0)
		}
	}
	if isAvailable || isExpired || expiringDays > 0 {
		query.Gt("certId", 0)

		if isAvailable {
			query.Where("certId IN (SELECT id FROM " + models.SharedSSLCertDAO.Table + " WHERE timeBeginAt<=UNIX_TIMESTAMP() AND timeEndAt>=UNIX_TIMESTAMP())")
		}
		if isExpired {
			query.Where("certId IN (SELECT id FROM " + models.SharedSSLCertDAO.Table + " WHERE timeEndAt<UNIX_TIMESTAMP())")
		}
		if expiringDays > 0 {
			query.Where("certId IN (SELECT id FROM "+models.SharedSSLCertDAO.Table+" WHERE timeEndAt>UNIX_TIMESTAMP() AND timeEndAt<:expiredAt)").
				Param("expiredAt", time.Now().Unix()+expiringDays*86400)
		}
	}

	if len(keyword) > 0 {
		query.Where("(domains LIKE :keyword)").
			Param("keyword", dbutils.QuoteLike(keyword))
	}
	if len(keyword) > 0 {
		query.Where("domains LIKE :keyword").
			Param("keyword", dbutils.QuoteLike(keyword))
	}

	return query.State(ACMETaskStateEnabled).
		Count()
}

// ListEnabledACMETasks 列出单页任务
func (this *ACMETaskDAO) ListEnabledACMETasks(tx *dbs.Tx, userId int64, isAvailable bool, isExpired bool, expiringDays int64, keyword string, userOnly bool, offset int64, size int64) (result []*ACMETask, err error) {
	var query = this.Query(tx)
	if userId > 0 {
		query.Attr("userId", userId)
	} else {
		if userOnly {
			query.Gt("userId", 0)
		} else {
			query.Attr("userId", 0)
		}
	}
	if isAvailable || isExpired || expiringDays > 0 {
		query.Gt("certId", 0)

		if isAvailable {
			query.Where("certId IN (SELECT id FROM " + models.SharedSSLCertDAO.Table + " WHERE timeBeginAt<=UNIX_TIMESTAMP() AND timeEndAt>=UNIX_TIMESTAMP())")
		}
		if isExpired {
			query.Where("certId IN (SELECT id FROM " + models.SharedSSLCertDAO.Table + " WHERE timeEndAt<UNIX_TIMESTAMP())")
		}
		if expiringDays > 0 {
			query.Where("certId IN (SELECT id FROM "+models.SharedSSLCertDAO.Table+" WHERE timeEndAt>UNIX_TIMESTAMP() AND timeEndAt<:expiredAt)").
				Param("expiredAt", time.Now().Unix()+expiringDays*86400)
		}
	}
	if len(keyword) > 0 {
		query.Where("(domains LIKE :keyword)").
			Param("keyword", dbutils.QuoteLike(keyword))
	}
	_, err = query.
		State(ACMETaskStateEnabled).
		DescPk().
		Offset(offset).
		Limit(size).
		Slice(&result).
		FindAll()
	return
}

// CreateACMETask 创建任务
func (this *ACMETaskDAO) CreateACMETask(tx *dbs.Tx, adminId int64, userId int64, authType acmeutils.AuthType, acmeUserId int64, dnsProviderId int64, dnsDomain string, domains []string, autoRenew bool, authURL string, async bool) (int64, error) {
	var op = NewACMETaskOperator()
	op.AdminId = adminId
	op.UserId = userId
	op.AuthType = authType
	op.AcmeUserId = acmeUserId
	op.DnsProviderId = dnsProviderId
	op.DnsDomain = dnsDomain

	if len(domains) > 0 {
		domainsJSON, err := json.Marshal(domains)
		if err != nil {
			return 0, err
		}
		op.Domains = domainsJSON
	} else {
		op.Domains = "[]"
	}

	op.AutoRenew = autoRenew
	op.AuthURL = authURL
	op.IsOn = true
	op.State = ACMETaskStateEnabled
	op.Async = async
	err := this.Save(tx, op)
	if err != nil {
		return 0, err
	}
	return types.Int64(op.Id), nil
}

// UpdateACMETask 修改任务
func (this *ACMETaskDAO) UpdateACMETask(tx *dbs.Tx, acmeTaskId int64, acmeUserId int64, dnsProviderId int64, dnsDomain string, domains []string, autoRenew bool, authURL string) error {
	if acmeTaskId <= 0 {
		return errors.New("invalid acmeTaskId")
	}

	var op = NewACMETaskOperator()
	op.Id = acmeTaskId
	op.AcmeUserId = acmeUserId
	op.DnsProviderId = dnsProviderId
	op.DnsDomain = dnsDomain

	if len(domains) > 0 {
		domainsJSON, err := json.Marshal(domains)
		if err != nil {
			return err
		}
		op.Domains = domainsJSON
	} else {
		op.Domains = "[]"
	}

	op.AutoRenew = autoRenew
	op.AuthURL = authURL
	err := this.Save(tx, op)
	return err
}

// CheckUserACMETask 检查用户权限
func (this *ACMETaskDAO) CheckUserACMETask(tx *dbs.Tx, userId int64, acmeTaskId int64) (bool, error) {
	var query = this.Query(tx)
	if userId > 0 {
		query.Attr("userId", userId)
	}

	return query.
		State(ACMETaskStateEnabled).
		Pk(acmeTaskId).
		Exist()
}

// FindACMETaskUserId 查找任务所属用户ID
func (this *ACMETaskDAO) FindACMETaskUserId(tx *dbs.Tx, taskId int64) (userId int64, err error) {
	return this.Query(tx).
		Pk(taskId).
		Result("userId").
		FindInt64Col(0)
}

// UpdateACMETaskCert 设置任务关联的证书
func (this *ACMETaskDAO) UpdateACMETaskCert(tx *dbs.Tx, taskId int64, certId int64) error {
	if taskId <= 0 {
		return errors.New("invalid taskId")
	}

	var op = NewACMETaskOperator()
	op.Id = taskId
	op.CertId = certId
	err := this.Save(tx, op)
	return err
}

// RunTask 执行任务并记录日志
func (this *ACMETaskDAO) RunTask(tx *dbs.Tx, taskId int64) (isOk bool, errMsg string, resultCertId int64) {
	isOk, errMsg, resultCertId = this.runTaskWithoutLog(tx, taskId, false)

	// 记录日志
	err := SharedACMETaskLogDAO.CreateACMETaskLog(tx, taskId, isOk, errMsg)
	if err != nil {
		logs.Error(err)
	}

	return
}

// 执行任务但并不记录日志
func (this *ACMETaskDAO) runTaskWithoutLog(tx *dbs.Tx, taskId int64, randomAcmeAccount bool) (isOk bool, errMsg string, resultCertId int64) {
	task, err := this.FindEnabledACMETask(tx, taskId)
	if err != nil {
		errMsg = "查询任务信息时出错：" + err.Error()
		return
	}
	if task == nil {
		errMsg = "找不到要执行的任务"
		return
	}
	if !task.IsOn {
		errMsg = "任务没有启用"
		return
	}

	if task.Status == ACMETaskStatusDone {
		errMsg = "任务已完成"
		return
	}

	// 设置执行中
	err = this.UpdateStatus(tx, taskId, ACMETaskStatusRunning)
	if err != nil {
		logs.Error(err)
	}

	// ACME用户
	user, err := SharedACMEUserDAO.FindEnabledACMEUser(tx, int64(task.AcmeUserId))
	if err != nil {
		errMsg = "查询ACME用户时出错：" + err.Error()
		return
	}
	if user == nil {
		errMsg = "找不到ACME用户"
		return
	}

	// 服务商
	if len(user.ProviderCode) == 0 {
		user.ProviderCode = acmeutils.DefaultProviderCode
	}

	if randomAcmeAccount {
		user, err = SharedACMEUserDAO.FindRandomACMEUserWithSameProvider(tx, user.ProviderCode)
		if user == nil {
			errMsg = "找不到ACME用户"
			return
		}
	}

	var acmeProvider = acmeutils.FindProviderWithCode(user.ProviderCode)
	if acmeProvider == nil {
		errMsg = "服务商已不可用"
		return
	}

	// 账号
	var acmeAccount *acmeutils.Account
	if user.AccountId > 0 {
		account, err := SharedACMEProviderAccountDAO.FindEnabledACMEProviderAccount(tx, int64(user.AccountId))
		if err != nil {
			errMsg = "查询ACME账号时出错：" + err.Error()
			return
		}
		if account != nil {
			acmeAccount = &acmeutils.Account{
				EABKid: account.EabKid,
				EABKey: account.EabKey,
			}
		}
	}

	privateKey, err := acmeutils.ParsePrivateKeyFromBase64(user.PrivateKey)
	if err != nil {
		errMsg = "解析私钥时出错：" + err.Error()
		return
	}

	var remoteUser = acmeutils.NewUser(user.Email, privateKey, func(resource *registration.Resource) error {
		resourceJSON, err := json.Marshal(resource)
		if err != nil {
			return err
		}

		err = SharedACMEUserDAO.UpdateACMEUserRegistration(tx, int64(user.Id), resourceJSON)
		return err
	})

	if len(user.Registration) > 0 {
		err = remoteUser.SetRegistration(user.Registration)
		if err != nil {
			errMsg = "设置注册信息时出错：" + err.Error()
			return
		}
	}

	var acmeTask *acmeutils.Task = nil
	if task.AuthType == acmeutils.AuthTypeDNS {
		// DNS服务商
		dnsProvider, err := dns.SharedDNSProviderDAO.FindEnabledDNSProvider(tx, int64(task.DnsProviderId))
		if err != nil {
			errMsg = "查找DNS服务商账号信息时出错：" + err.Error()
			return
		}
		if dnsProvider == nil {
			errMsg = "找不到DNS服务商账号"
			return
		}
		providerInterface := dnsclients.FindProvider(dnsProvider.Type, int64(dnsProvider.Id))
		if providerInterface == nil {
			errMsg = "暂不支持此类型的DNS服务商 '" + dnsProvider.Type + "'"
			return
		}
		providerInterface.SetMinTTL(int32(dnsProvider.MinTTL))
		apiParams, err := dnsProvider.DecodeAPIParams()
		if err != nil {
			errMsg = "解析DNS服务商API参数时出错：" + err.Error()
			return
		}
		err = providerInterface.Auth(apiParams)
		if err != nil {
			errMsg = "校验DNS服务商API参数时出错：" + err.Error()
			return
		}

		acmeTask = &acmeutils.Task{
			User:        remoteUser,
			AuthType:    acmeutils.AuthTypeDNS,
			DNSProvider: providerInterface,
			DNSDomain:   task.DnsDomain,
			Domains:     task.DecodeDomains(),
		}
	} else if task.AuthType == acmeutils.AuthTypeHTTP {
		acmeTask = &acmeutils.Task{
			User:     remoteUser,
			AuthType: acmeutils.AuthTypeHTTP,
			Domains:  task.DecodeDomains(),
		}
	}
	acmeTask.Provider = acmeProvider
	acmeTask.Account = acmeAccount

	var acmeRequest = acmeutils.NewRequest(acmeTask)
	acmeRequest.OnAuth(func(domain, token, keyAuth string) {
		err := SharedACMEAuthenticationDAO.CreateAuth(tx, taskId, domain, token, keyAuth)
		if err != nil {
			remotelogs.Error("ACME", "write authentication to database error: "+err.Error())
		} else {
			// 调用校验URL
			if len(task.AuthURL) > 0 {
				authJSON, err := json.Marshal(maps.Map{
					"domain": domain,
					"token":  token,
					"key":    keyAuth,
				})
				if err != nil {
					remotelogs.Error("ACME", "encode auth data failed: '"+task.AuthURL+"'")
				} else {
					var client = utils.SharedHttpClient(10 * time.Second)
					req, err := http.NewRequest(http.MethodPost, task.AuthURL, bytes.NewReader(authJSON))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("User-Agent", teaconst.ProductName+"/"+teaconst.Version)
					if err != nil {
						remotelogs.Error("ACME", "parse auth url failed '"+task.AuthURL+"': "+err.Error())
					} else {
						resp, err := client.Do(req)
						if err != nil {
							remotelogs.Error("ACME", "call auth url failed '"+task.AuthURL+"': "+err.Error())
						} else {
							_ = resp.Body.Close()
						}
					}
				}
			}
		}
	})
	certData, keyData, err := acmeRequest.Run()
	if err != nil {
		errMsg = "证书生成失败：" + err.Error()
		return
	}

	// 分析证书
	var sslConfig = &sslconfigs.SSLCertConfig{
		CertData: certData,
		KeyData:  keyData,
	}
	err = sslConfig.Init(context.Background())
	if err != nil {
		errMsg = "证书生成成功，但是分析证书信息时发生错误：" + err.Error()
		return
	}

	// 保存证书
	resultCertId = int64(task.CertId)
	if resultCertId > 0 {
		cert, err := models.SharedSSLCertDAO.FindEnabledSSLCert(tx, resultCertId)
		if err != nil {
			errMsg = "证书生成成功，但查询已绑定的证书时出错：" + err.Error()
			return
		}
		if cert == nil {
			errMsg = "证书已被管理员或用户删除"

			// 禁用
			err = SharedACMETaskDAO.DisableACMETask(tx, taskId)
			if err != nil {
				errMsg = "禁用失效的ACME任务出错：" + err.Error()
			}

			return
		}

		err = models.SharedSSLCertDAO.UpdateCert(tx, resultCertId, cert.IsOn, cert.Name, cert.Description, cert.ServerName, cert.IsCA, certData, keyData, sslConfig.TimeBeginAt, sslConfig.TimeEndAt, sslConfig.DNSNames, sslConfig.CommonNames)
		if err != nil {
			errMsg = "证书生成成功，但是修改数据库中的证书信息时出错：" + err.Error()
			return
		}
	} else {
		resultCertId, err = models.SharedSSLCertDAO.CreateCert(tx, int64(task.AdminId), int64(task.UserId), true, task.DnsDomain+"免费证书", "免费申请的证书", "", false, certData, keyData, sslConfig.TimeBeginAt, sslConfig.TimeEndAt, sslConfig.DNSNames, sslConfig.CommonNames)
		if err != nil {
			errMsg = "证书生成成功，但是保存到数据库失败：" + err.Error()
			return
		}

		err = models.SharedSSLCertDAO.UpdateCertACME(tx, resultCertId, int64(task.Id))
		if err != nil {
			errMsg = "证书生成成功，修改证书ACME信息时出错：" + err.Error()
			return
		}

		// 设置成功
		err = SharedACMETaskDAO.UpdateACMETaskCert(tx, taskId, resultCertId)
		if err != nil {
			errMsg = "证书生成成功，设置任务关联的证书时出错：" + err.Error()
			return
		}
	}

	isOk = true
	return
}

// FindIssueACMETask 查找N小时内未执行的AcmeTask
func (this *ACMETaskDAO) FindIssueACMETask(tx *dbs.Tx, hour int, limit int64, excludeTasks []int64) (result []*ACMETask, err error) {
	if len(excludeTasks) == 0 {
		excludeTasks = append(excludeTasks, 0)
	}
	var strIDs []string
	for _, id := range excludeTasks {
		strIDs = append(strIDs, strconv.FormatInt(id, 10))
	}
	_, err = this.Query(tx).
		Attr("isOn", true).
		Attr("async", true).
		State(ACMETaskStateEnabled).
		Where("FROM_UNIXTIME(createdAt, '%Y-%m-%d %H:%i')>:hoursAgo AND certId=0 and id NOT IN ("+strings.Join(strIDs, ",")+")").
		Param("hoursAgo", time.Now().UTC().Add(-time.Duration(hour)*time.Hour).Format("2006-01-02 15:04")).
		Param("now", time.Now().Unix()).
		Slice(&result).
		AscPk().
		Limit(limit).
		FindAll()
	return
}

// UpdateStatus 更新状态
func (this *ACMETaskDAO) UpdateStatus(tx *dbs.Tx, id int64, status int64) error {
	_, err := this.Query(tx).
		Pk(id).
		Set("status", status).
		Update()
	return err
}

// RunTaskAndAutoBindServer 证书签发并绑定Server，记录日志
func (this *ACMETaskDAO) RunTaskAndAutoBindServer(tx *dbs.Tx, taskId int64, domains []string) (isOk bool, errMsg string) {
	_, ok := runningTaskMap.Load(taskId)
	if ok {
		return true, "" // 返回ok，异步任务无需继续执行
	}

	isOk, errMsg, resultCertId := this.runTaskWithoutLog(tx, taskId, true)

	// 记录日志
	err := SharedACMETaskLogDAO.CreateACMETaskLog(tx, taskId, isOk, errMsg)
	if err != nil {
		logs.Error(err)
	}
	if !isOk {
		// 设置签发失败
		err = this.UpdateStatus(tx, taskId, ACMETaskStatusIssueFailed)
		if err != nil {
			logs.Error(err)
		}
		return
	}

	// 签发成功
	err = this.UpdateStatus(tx, taskId, ACMETaskStatusDone)
	if err != nil {
		logs.Error(err)
	}

	newCert, err := models.SharedSSLCertDAO.FindEnabledSSLCert(tx, resultCertId)
	if err != nil {
		logs.Error(err)
		return
	}

	type ServerInfo struct {
		SSLPolicyId int64
		CertIds     []int64
		UserId      int64
		TlsConfig   *serverconfigs.HTTPSProtocolConfig
	}
	serverMap := map[int64]ServerInfo{}
	domainChecked := map[string]bool{}

	// 以下绑定cert到Server的逻辑加互斥锁，避免大量证书绑定到同一个Server时sslCertIds数据覆盖；以下绑定流程耗时不长
	serverBindMutex.Lock()
	defer serverBindMutex.Unlock()

	// 获取域名需要绑定的SSLPolicy
	for _, domain := range domains {
		if _, ok := domainChecked[domain]; ok {
			continue
		}
		servers, err := models.SharedServerDAO.FindUserServerByServerName(tx, domain)
		if err != nil {
			continue
		}
		for _, server := range servers {
			var serverNames []string
			err = json.Unmarshal(server.PlainServerNames, &serverNames)
			if err != nil {
				continue
			}
			for _, sn := range serverNames {
				domainChecked[sn] = true
			}
			tlsConfig := server.DecodeHTTPS()
			if tlsConfig == nil {
				continue // 跳过，其他有HTTPS的正常执行
			}
			if tlsConfig.SSLPolicyRef != nil {
				sslPolicyConfig, err := models.SharedSSLPolicyDAO.ComposePolicyConfig(tx, tlsConfig.SSLPolicyRef.SSLPolicyId, false, nil, nil)
				if err != nil {
					continue
				}
				if sslPolicyConfig != nil {
					var certIds []int64
					for _, cert := range sslPolicyConfig.Certs {
						// 新签发的证书如果包含所有旧证书的域名则不再绑定旧证书，并禁用旧证书避免触发续签
						if utils.ListIsGreaterEqualThanOther(domains, cert.DNSNames) && cert.TimeEndAt < int64(newCert.TimeEndAt) {
							err = models.SharedSSLCertDAO.DisableSSLCert(tx, cert.Id)
							if err != nil {
								logs.Error(err)
							}
							continue
						}
						certIds = append(certIds, cert.Id)
					}
					certIds = append(certIds, resultCertId)
					serverMap[int64(server.Id)] = ServerInfo{
						UserId:      int64(server.UserId),
						SSLPolicyId: sslPolicyConfig.Id,
						CertIds:     certIds,
						TlsConfig:   tlsConfig}
					continue
				}
			}
			serverMap[int64(server.Id)] = ServerInfo{CertIds: []int64{resultCertId}, TlsConfig: tlsConfig, UserId: int64(server.UserId)}
		}
	}

	for serverId, serverInfo := range serverMap {
		var certRefs []*sslconfigs.SSLCertRef
		certExists := make(map[int64]bool)
		for _, certId := range serverInfo.CertIds {
			if !certExists[certId] {
				certRefs = append(certRefs, &sslconfigs.SSLCertRef{
					IsOn:   true,
					CertId: certId,
				})
				certExists[certId] = true
			}
		}

		certRefsJSON, err := json.Marshal(certRefs)
		if err != nil {
			logs.Errorf("解析证书错误：%s", err.Error())
			continue
		}
		if serverInfo.SSLPolicyId == 0 {
			policyId, err := models.SharedSSLPolicyDAO.CreatePolicy(tx,
				0, serverInfo.UserId, false, false,
				"TLS 1.1", certRefsJSON,
				nil, false, 0,
				nil, false, nil)
			if err != nil {
				logs.Errorf("创建SSL策略错误：%s", err.Error())
				continue
			}
			httpsConfig := serverInfo.TlsConfig
			httpsConfig.SSLPolicyRef = &sslconfigs.SSLPolicyRef{
				IsOn:        true,
				SSLPolicyId: policyId,
			}
			httpsJSON, err := json.Marshal(httpsConfig)
			if err != nil {
				logs.Errorf("获取https信息错误：%s", err.Error())
				continue
			}
			err = models.SharedServerDAO.UpdateServerHTTPS(tx, serverId, httpsJSON)
		} else {
			policy, err := models.SharedSSLPolicyDAO.FindEnabledSSLPolicy(tx, serverInfo.SSLPolicyId)
			if err != nil {
				logs.Errorf("获取SSL策略错误：%s", err.Error())
				continue
			}
			err = models.SharedSSLPolicyDAO.UpdatePolicy(tx, serverInfo.SSLPolicyId, policy.Http2Enabled, policy.Http3Enabled,
				policy.MinVersion, certRefsJSON, policy.Hsts, policy.OcspIsOn == 1, int32(policy.ClientAuthType), policy.ClientCACerts, policy.CipherSuitesIsOn == 1, nil)
			if err != nil {
				logs.Errorf("更新SSL策略错误：%s", err.Error())
				continue
			}

		}
	}
	return
}
