package constants

// 设备状态枚举。
const (
	DeviceStatusInStorage        = "in_storage"         // 在库
	DeviceStatusInUse            = "in_use"             // 使用中
	DeviceStatusUnderMaintenance = "under_maintenance"  // 维修中（存在未闭环维修工单）
	DeviceStatusUnavailable      = "unavailable"        // 不可用（恢复使用条件未全部满足，如计量不合格/已过期/未计量）
	DeviceStatusDisabled         = "disabled"           // 已禁用
	DeviceStatusScrapped         = "scrapped"           // 已报废
)

// MaintenanceUnfinishedStatuses 未闭环（处理中）的工单状态集合。
// 注意：业务上"处理中"包含"待处理"与"处理中"，已完成/已取消的工单不再阻塞设备恢复使用。
var MaintenanceUnfinishedStatuses = []string{
	MaintenanceStatusPending,
	MaintenanceStatusInProgress,
}

// 采购申请状态枚举。
const (
	PurchaseStatusPendingDeviceAdmin = "pending_device_admin" // 待设备科审核
	PurchaseStatusPendingDean        = "pending_dean"         // 待院长审批
	PurchaseStatusApproved           = "approved"             // 审批通过
	PurchaseStatusDelivered          = "delivered"            // 已到货待验收
	PurchaseStatusAccepted           = "accepted"             // 已验收
	PurchaseStatusRejected           = "rejected"             // 已拒绝
)

// 保养/维修类型枚举。
const (
	MaintenanceTypeDaily   = "daily"   // 日检
	MaintenanceTypeWeekly  = "weekly"  // 周检
	MaintenanceTypeMonthly = "monthly" // 月检
	MaintenanceTypeYearly  = "yearly"  // 年检
	MaintenanceTypeRepair  = "repair"  // 故障维修
)

// 保养/维修状态枚举。
const (
	MaintenanceStatusPending    = "pending"    // 待处理
	MaintenanceStatusInProgress = "in_progress" // 处理中
	MaintenanceStatusCompleted  = "completed"  // 已完成
	MaintenanceStatusCancelled  = "cancelled"  // 已取消
)

// 计量状态枚举。
const (
	CalibrationStatusNormal     = "normal"      // 合格
	CalibrationStatusUnqualified = "unqualified" // 不合格
	CalibrationStatusDue        = "due"         // 即将到期
	CalibrationStatusExpired    = "expired"     // 已过期
)

// 调拨状态枚举。
const (
	TransferStatusPending  = "pending"
	TransferStatusApproved = "approved"
	TransferStatusRejected = "rejected"
)

// 报废状态枚举。
const (
	ScrapStatusPending  = "pending"
	ScrapStatusApproved = "approved"
	ScrapStatusRejected = "rejected"
)

// 用户状态枚举。
const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
)

// 计量结果枚举。
const (
	CalibrationResultQualified   = "qualified"
	CalibrationResultUnqualified = "unqualified"
)
