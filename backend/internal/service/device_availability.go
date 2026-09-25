package service

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/medasset/medasset/internal/constants"
	"github.com/medasset/medasset/internal/model"
	"github.com/medasset/medasset/internal/repository"
	"gorm.io/gorm"
)

// DeviceAvailabilityService 设备恢复使用联合判定服务。
// 维修完成与计量结果登记两侧必须复用本服务，避免"各自把设备置为使用中"的互相覆盖：
// 恢复使用需同时满足 ① 无处理中的维修工单；② 有计量要求的设备存在未过期且合格的计量记录。
type DeviceAvailabilityService struct {
	deviceRepo      *repository.DeviceRepository
	maintenanceRepo *repository.MaintenanceRepository
	calibrationRepo *repository.CalibrationRepository
	log             *slog.Logger
}

func NewDeviceAvailabilityService(
	deviceRepo *repository.DeviceRepository,
	maintenanceRepo *repository.MaintenanceRepository,
	calibrationRepo *repository.CalibrationRepository,
	log *slog.Logger,
) *DeviceAvailabilityService {
	return &DeviceAvailabilityService{
		deviceRepo:      deviceRepo,
		maintenanceRepo: maintenanceRepo,
		calibrationRepo: calibrationRepo,
		log:             log,
	}
}

// AvailabilityOutcome 恢复使用联合判定结果。
type AvailabilityOutcome struct {
	Ready  bool   // 两项条件是否同时满足
	Status string // Ready=true 时为 in_use；否则为设备应保持的不可用状态
	Note   string // 写在设备台账/维修工单/计量记录上的说明（还缺哪项）
}

// EvaluateTx 在事务内执行联合判定。
// 调用方必须已使用 DeviceRepository.FindByIDForUpdate 锁定设备行：
// 维修与计量两侧办理时都先锁设备行再锁各自子记录，并发办理由设备行锁串行化，互不覆盖。
func (s *DeviceAvailabilityService) EvaluateTx(tx *gorm.DB, d *model.Device, now time.Time) (*AvailabilityOutcome, error) {
	openRepair, err := s.maintenanceRepo.ExistsActiveRepairTx(tx, d.ID)
	if err != nil {
		return nil, err
	}
	calibrationOK := true
	if d.CalibrationRequired {
		exists, err := s.calibrationRepo.ExistsValidQualifiedTx(tx, d.ID, now)
		if err != nil {
			return nil, err
		}
		calibrationOK = exists
	}

	if !openRepair && calibrationOK {
		return &AvailabilityOutcome{
			Ready:  true,
			Status: constants.DeviceStatusInUse,
			Note:   constants.MsgAvailabilityReady,
		}, nil
	}

	var missing []string
	if openRepair {
		missing = append(missing, constants.MsgAvailabilityRepairOpen)
	}
	if d.CalibrationRequired && !calibrationOK {
		missing = append(missing, constants.MsgAvailabilityCalibInvalid)
	}
	note := strings.Join(missing, "；")

	// 仍有维修工单未闭环 → 保持"维修中"；维修已闭环但计量不满足 → 不可用。
	status := constants.DeviceStatusUnavailable
	if openRepair {
		status = constants.DeviceStatusUnderMaintenance
	}
	// 设备已因计量不合格被禁用、且计量条件仍不满足时，保持"已禁用"，
	// 不被维修完成/取消等另一侧的动作覆盖；计量重新登记合格后才会恢复。
	if d.Status == constants.DeviceStatusDisabled && d.CalibrationRequired && !calibrationOK {
		status = constants.DeviceStatusDisabled
	}
	return &AvailabilityOutcome{Ready: false, Status: status, Note: note}, nil
}

// ApplyTx 将判定结果落库到设备台账（状态 + 恢复条件说明），并记录状态变更日志。
func (s *DeviceAvailabilityService) ApplyTx(tx *gorm.DB, d *model.Device, outcome *AvailabilityOutcome) error {
	if err := s.deviceRepo.UpdateAvailabilityTx(tx, d.ID, outcome.Status, outcome.Note); err != nil {
		return err
	}
	d.Status = outcome.Status
	d.AvailabilityNote = outcome.Note
	s.log.Info(fmt.Sprintf(constants.LogDeviceAvailabilityRechecked, d.ID, outcome.Ready, outcome.Status, outcome.Note))
	return nil
}
