package service

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/medasset/medasset/internal/constants"
	"github.com/medasset/medasset/internal/model"
	"github.com/medasset/medasset/internal/repository"
	"gorm.io/gorm"
)

// TriggerSource 设备恢复使用综合判定的触发来源（维修侧/计量侧），用于日志与台账留痕。
const (
	RecoverTriggerMaintenance = "maintenance_complete"
	RecoverTriggerCalibration = "calibration_result"
)

// DeviceAvailabilityService 设备恢复使用综合判定服务。
//
// 设备恢复使用必须同时满足：
//  1. 没有未办结（待处理/处理中）的故障维修工单；
//  2. 有计量要求的设备，存在未过期且结果合格的计量记录。
//
// 维修完成与计量结果登记都复用本服务的 RecheckTx：各自完成后都重新判定一次，
// 满足两项才转为“使用中”，否则保持不可用并写明还缺哪项。判定在单事务内对
// 设备行加 FOR UPDATE 行锁后执行，两边同时办理时后到的事务会读到对方的提交，
// 不会互相覆盖。
type DeviceAvailabilityService struct {
	device      *repository.DeviceRepository
	maintenance *repository.MaintenanceRepository
	calibration *repository.CalibrationRepository
	log         *slog.Logger
}

func NewDeviceAvailabilityService(device *repository.DeviceRepository,
	maintenance *repository.MaintenanceRepository, calibration *repository.CalibrationRepository,
	log *slog.Logger) *DeviceAvailabilityService {
	return &DeviceAvailabilityService{device: device, maintenance: maintenance, calibration: calibration, log: log}
}

// RecheckResult 判定结果。
type RecheckResult struct {
	Ready          bool     // 两项条件是否同时满足
	RepairBlocked  bool     // 是否被未办结维修工单阻断
	CalibBlocked   bool     // 是否被计量条件阻断
	MissingReasons []string // 仍缺失的条件文案（台账/维修/计量三处展示同一份说明）
	PreviousStatus string   // 判定前设备状态
	CurrentStatus  string   // 判定后设备状态
}

// RecheckTx 在已有事务内重新判定设备是否可恢复使用，并据此更新设备状态与台账说明。
// 调用方必须先在同一事务中完成本侧业务数据（工单/计量记录）的落库；
// 且本方法第一个加锁对象必须是设备行，保证维修侧与计量侧并发时加锁顺序一致。
func (s *DeviceAvailabilityService) RecheckTx(tx *gorm.DB, deviceID uint, trigger string) (*RecheckResult, error) {
	// 1) 先锁设备行：所有恢复判定路径统一的加锁顺序，避免两边同时办理时互相覆盖/死锁。
	d, err := s.device.FindByIDForUpdate(tx, deviceID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("lock device for recover check: %w", err)
	}
	previous := d.Status
	result := &RecheckResult{PreviousStatus: previous, MissingReasons: []string{}}

	// 已报废设备不参与恢复判定，状态保持不变。
	if previous == constants.DeviceStatusScrapped {
		result.CurrentStatus = previous
		return result, nil
	}

	// 2) 条件一：不存在未办结（待处理/处理中）的故障维修工单。
	openRepairs, err := s.maintenance.CountOpenRepairForUpdate(tx, deviceID)
	if err != nil {
		return nil, err
	}
	result.RepairBlocked = openRepairs > 0
	if result.RepairBlocked {
		result.MissingReasons = append(result.MissingReasons, constants.RecoverBlockRepair)
	}

	// 3) 条件二：有计量要求的设备，需存在“未过期且结果合格”的最近一次计量记录。
	if d.CalibrationRequired {
		latest, cErr := s.calibration.LatestByDeviceForUpdate(tx, deviceID)
		switch {
		case errors.Is(cErr, repository.ErrNotFound):
			result.CalibBlocked = true
			result.MissingReasons = append(result.MissingReasons, constants.RecoverBlockCalibRequired)
		case cErr != nil:
			return nil, fmt.Errorf("load latest calibration for recover check: %w", cErr)
		default:
			if reason, blocked := calibrationBlockReason(latest, time.Now()); blocked {
				result.CalibBlocked = true
				result.MissingReasons = append(result.MissingReasons, reason)
			}
		}
	}

	result.Ready = !result.RepairBlocked && !result.CalibBlocked
	now := time.Now()
	if result.Ready {
		result.CurrentStatus = constants.DeviceStatusInUse
	} else {
		result.CurrentStatus = constants.DeviceStatusUnavailable
	}
	reason := strings.Join(result.MissingReasons, "；")

	// 4) 单条 UPDATE 原子写入状态+缺失说明+判定时间，杜绝“只改了状态没写原因”。
	if err := s.device.UpdateRecoverStateTx(tx, deviceID, result.CurrentStatus, reason, now); err != nil {
		return nil, err
	}

	s.log.Info(fmt.Sprintf(constants.LogDeviceRecoverCheck,
		deviceID, result.CurrentStatus, result.RepairBlocked, result.CalibBlocked, reason, trigger))
	return result, nil
}

// NoteText 返回写入维修工单/计量记录的判定说明（业务侧的人类可读留痕）。
func NoteText(r *RecheckResult) string {
	if r.Ready {
		return constants.RecoverReady
	}
	return "设备暂不可用，缺少：" + strings.Join(r.MissingReasons, "；")
}

// calibrationBlockReason 返回计量维度阻断原因；无阻断时 blocked=false。
func calibrationBlockReason(c *model.CalibrationRecord, now time.Time) (string, bool) {
	if c.Result != constants.CalibrationResultQualified {
		return constants.RecoverBlockCalibUnqualified, true
	}
	if c.NextCalibrationDate == nil || !c.NextCalibrationDate.After(now) {
		return constants.RecoverBlockCalibExpired, true
	}
	return "", false
}
