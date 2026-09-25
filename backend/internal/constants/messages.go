package constants

// 接口返回文案集中维护（同时被日志与错误提示复用）。
const (
	MsgOK                    = "ok"
	MsgInvalidParams         = "请求参数不合法"
	MsgUnauthorized          = "未登录或登录已过期"
	MsgForbidden             = "无权访问该资源"
	MsgNotFound              = "资源不存在"
	MsgInternalError         = "服务器内部错误"
	MsgWrongPassword         = "用户名或密码错误"
	MsgUserDisabled          = "账号已被禁用，请联系管理员"
	MsgInvalidStatus         = "当前状态不允许执行该操作"
	MsgDuplicateUser         = "用户名已存在"
	MsgDuplicateAssetCode    = "资产编号已存在"
	MsgDuplicateRequestNo    = "申请单号已存在"
	MsgDuplicateInstrumentNo = "计量器具编号已存在"
	MsgDeviceNotAllowed      = "该设备状态不允许执行此操作"
	MsgDeviceInScrapped      = "已报废设备不可操作"
	MsgRateLimited           = "请求过于频繁，请稍后再试"
	MsgInvalidToken          = "无效的访问令牌"
	MsgTokenExpired          = "访问令牌已过期"
)

// 设备恢复使用综合判定的缺失条件说明（同时用于设备台账、维修工单、计量记录）。
const (
	// RecoverBlockRepair 仍有处理中的维修工单。
	RecoverBlockRepair = "存在处理中的维修工单"
	// RecoverBlockCalibRequired 设备有计量要求但尚无计量记录。
	RecoverBlockCalibRequired = "该设备有计量要求，尚无合格计量记录"
	// RecoverBlockCalibExpired 计量记录已过期（下次计量日期已到）。
	RecoverBlockCalibExpired = "计量记录已过期，需重新计量合格后方可投用"
	// RecoverBlockCalibUnqualified 最近一次计量结果不合格。
	RecoverBlockCalibUnqualified = "最近一次计量结果不合格"
	// RecoverReady 两项条件均满足。
	RecoverReady = "维修工单均已办结且计量合格有效，设备可恢复使用"
)
