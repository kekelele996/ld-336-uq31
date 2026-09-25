package constants

// 接口返回文案集中维护（同时被日志与错误提示复用）。
const (
	MsgOK                   = "ok"
	MsgInvalidParams        = "请求参数不合法"
	MsgUnauthorized         = "未登录或登录已过期"
	MsgForbidden            = "无权访问该资源"
	MsgNotFound             = "资源不存在"
	MsgInternalError        = "服务器内部错误"
	MsgWrongPassword        = "用户名或密码错误"
	MsgUserDisabled         = "账号已被禁用，请联系管理员"
	MsgInvalidStatus        = "当前状态不允许执行该操作"
	MsgDuplicateUser        = "用户名已存在"
	MsgDuplicateAssetCode   = "资产编号已存在"
	MsgDuplicateRequestNo   = "申请单号已存在"
	MsgDuplicateInstrumentNo = "计量器具编号已存在"
	MsgDeviceNotAllowed     = "该设备状态不允许执行此操作"
	MsgDeviceInScrapped     = "已报废设备不可操作"
	MsgAvailabilityReady    = "设备已满足恢复使用条件：维修工单均已闭环，计量记录未过期且结果合格"
	MsgAvailabilityRepairOpen    = "存在处理中的维修工单，设备暂不可用"
	MsgAvailabilityCalibMissing  = "该设备有计量要求但缺少计量记录，设备暂不可用"
	MsgAvailabilityCalibInvalid  = "缺少未过期且结果合格的计量记录（未计量/已过期/不合格），设备暂不可用"
	MsgAvailabilityCalibUnqualified = "计量结果不合格，设备已禁用"
	MsgRateLimited          = "请求过于频繁，请稍后再试"
	MsgInvalidToken         = "无效的访问令牌"
	MsgTokenExpired         = "访问令牌已过期"
)
