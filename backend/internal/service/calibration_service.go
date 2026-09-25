package service

import (
	"fmt"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/medasset/medasset/internal/constants"
	"github.com/medasset/medasset/internal/dto"
	"github.com/medasset/medasset/internal/model"
	"github.com/medasset/medasset/internal/repository"
	"github.com/medasset/medasset/internal/util"
	"gorm.io/gorm"
)

// CalibrationService 计量与质控服务。
type CalibrationService struct {
	repo            *repository.CalibrationRepository
	device          *repository.DeviceRepository
	maintenanceRepo *repository.MaintenanceRepository
	availability    *DeviceAvailabilityService
	audit           *AuditService
	log             *slog.Logger
}

func NewCalibrationService(repo *repository.CalibrationRepository, device *repository.DeviceRepository, maintenanceRepo *repository.MaintenanceRepository, availability *DeviceAvailabilityService, audit *AuditService, log *slog.Logger) *CalibrationService {
	return &CalibrationService{repo: repo, device: device, maintenanceRepo: maintenanceRepo, availability: availability, audit: audit, log: log}
}

// Create 建立计量台账。
func (s *CalibrationService) Create(req *dto.CreateCalibrationReq, operator string) (*model.CalibrationRecord, error) {
	taken, err := s.repo.IsInstrumentNoTaken(req.InstrumentNo)
	if err != nil {
		return nil, util.NewAppError(http.StatusInternalServerError, constants.MsgInternalError, err)
	}
	if taken {
		return nil, util.NewAppError(http.StatusConflict, constants.MsgDuplicateInstrumentNo, nil)
	}
	d, err := s.device.FindByID(req.DeviceID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, util.NewAppError(http.StatusNotFound, "设备不存在: device_id="+util.Uint64String(req.DeviceID), nil)
	}
	if err != nil {
		return nil, util.NewAppError(http.StatusInternalServerError, constants.MsgInternalError, err)
	}
	now := time.Now()
	last := req.LastCalibrationDate
	if last == nil {
		last = &now
	}
	next := last.AddDate(0, req.CalibrationCycleMonths, 0)
	c := &model.CalibrationRecord{
		InstrumentNo:            req.InstrumentNo,
		DeviceID:                d.ID,
		DeviceName:              d.Name,
		CalibrationCycleMonths:  req.CalibrationCycleMonths,
		LastCalibrationDate:     last,
		NextCalibrationDate:     &next,
		Status:                  constants.CalibrationStatusNormal,
		Result:                  constants.CalibrationResultQualified,
		CertificateNo:           req.CertificateNo,
		CalibrationOrg:          req.CalibrationOrg,
		Remark:                  req.Remark,
		CreatedBy:               operator,
	}
	if err := s.repo.Create(c); err != nil {
		return nil, util.NewAppError(http.StatusInternalServerError, "建立计量台账失败: instrument_no="+req.InstrumentNo, err)
	}
	s.log.Info(fmt.Sprintf(constants.LogCalibrationCreated, c.InstrumentNo, c.DeviceID, c.CalibrationCycleMonths, c.Status))
	s.audit.Record(0, operator, "CREATE", "calibration", util.Uint64String(c.ID), "建立计量台账: "+c.InstrumentNo, operator, "")
	return c, nil
}

// List 分页查询计量记录。
func (s *CalibrationService) List(page, pageSize int, deviceID uint, status string) (*util.PageResult, error) {
	list, total, err := s.repo.List(page, pageSize, deviceID, status)
	if err != nil {
		return nil, util.NewAppError(http.StatusInternalServerError, constants.MsgInternalError, err)
	}
	return &util.PageResult{List: list, Total: total, Page: page, PageSize: pageSize}, nil
}

// DueList 计量到期预警清单（被列表页与统计接口复用）。
func (s *CalibrationService) DueList() ([]model.CalibrationRecord, error) {
	list, err := s.repo.ListDue()
	if err != nil {
		return nil, util.NewAppError(http.StatusInternalServerError, constants.MsgInternalError, err)
	}
	return list, nil
}

// RecordResult 登记计量结果。
// 合格：重新执行恢复使用联合判定（无处理中维修工单 且 计量未过期合格才转为使用中）；
// 不合格：设备保持禁用，并在计量记录与设备台账上说明原因。
// 事务按"先锁设备行、再锁计量记录行"的固定顺序加锁，与维修侧互斥，并发办理不互相覆盖。
func (s *CalibrationService) RecordResult(id uint, req *dto.CalibrationResultReq, operator string) (*model.CalibrationRecord, error) {
	var updated *model.CalibrationRecord
	err := s.repo.DB().Transaction(func(tx *gorm.DB) error {
		pre, err := s.repo.FindByIDTx(tx, id)
		if errors.Is(err, repository.ErrNotFound) {
			return util.NewAppError(http.StatusNotFound, "计量记录不存在: id="+util.Uint64String(id), nil)
		}
		if err != nil {
			return err
		}
		d, err := s.device.FindByIDForUpdate(tx, pre.DeviceID)
		if err != nil {
			return err
		}
		c, err := s.repo.FindByIDForUpdate(tx, id)
		if err != nil {
			return err
		}
		now := time.Now()
		next := req.NextCalibrationDate
		if next == nil {
			nextDate := now.AddDate(0, c.CalibrationCycleMonths, 0)
			next = &nextDate
		}
		c.NextCalibrationDate = next
		c.LastCalibrationDate = &now
		c.CertificateNo = req.CertificateNo
		c.CalibrationOrg = req.CalibrationOrg
		c.Remark = req.Remark
		if d.Status == constants.DeviceStatusScrapped {
			return util.NewAppError(http.StatusConflict, constants.MsgDeviceInScrapped, nil)
		}
		if req.Result == constants.CalibrationResultQualified {
			c.Status = constants.CalibrationStatusNormal
			c.Result = constants.CalibrationResultQualified
		} else {
			c.Status = constants.CalibrationStatusUnqualified
			c.Result = constants.CalibrationResultUnqualified
		}
		// 先落库本次计量结果与下次计量日期，联合判定 COUNT 计量记录时才能读到最新值。
		if err := s.repo.UpdateTx(tx, c); err != nil {
			return err
		}
		if req.Result == constants.CalibrationResultQualified {
			// 合格也不能直接投用：维修工单未闭环时仍保持不可用，说明还缺哪项。
			outcome, err := s.availability.EvaluateTx(tx, d, now)
			if err != nil {
				return err
			}
			c.AvailabilityNote = outcome.Note
			if err := s.availability.ApplyTx(tx, d, outcome); err != nil {
				return err
			}
		} else {
			note := constants.MsgAvailabilityCalibUnqualified
			// 若同时还有处理中的维修工单，也要在说明里指出，避免只看到"计量不合格"。
			openRepair, err := s.maintenanceOpen(tx, d.ID)
			if err != nil {
				return err
			}
			if openRepair {
				note = constants.MsgAvailabilityCalibUnqualified + "；" + constants.MsgAvailabilityRepairOpen
			}
			c.AvailabilityNote = note
			if err := s.device.UpdateAvailabilityTx(tx, c.DeviceID, constants.DeviceStatusDisabled, note); err != nil {
				return err
			}
			d.Status = constants.DeviceStatusDisabled
			d.AvailabilityNote = note
		}
		// 回写判定说明到计量记录（计量台账与设备台账均说明还缺哪项）。
		if err := s.repo.UpdateTx(tx, c); err != nil {
			return err
		}
		updated = c
		return nil
	})
	if err != nil {
		return nil, wrapSvcErr(err)
	}
	s.log.Info(fmt.Sprintf(constants.LogCalibrationResult, updated.InstrumentNo, updated.Result, updated.Status, updated.DeviceID))
	s.audit.Record(0, operator, "RESULT", "calibration", util.Uint64String(updated.ID), "登记计量结果: "+updated.InstrumentNo+"="+updated.Result+"；恢复判定: "+updated.AvailabilityNote, operator, "")
	return updated, nil
}

// maintenanceOpen 检查设备是否存在未闭环的故障维修工单（与联合判定同一口径）。
func (s *CalibrationService) maintenanceOpen(tx *gorm.DB, deviceID uint) (bool, error) {
	return s.maintenanceRepo.ExistsActiveRepairTx(tx, deviceID)
}
