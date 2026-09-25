package service

import (
	"errors"
	"fmt"
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
	repo         *repository.CalibrationRepository
	device       *repository.DeviceRepository
	availability *DeviceAvailabilityService
	audit        *AuditService
	log          *slog.Logger
}

func NewCalibrationService(repo *repository.CalibrationRepository, device *repository.DeviceRepository,
	availability *DeviceAvailabilityService, audit *AuditService, log *slog.Logger) *CalibrationService {
	return &CalibrationService{repo: repo, device: device, availability: availability, audit: audit, log: log}
}

// Create 建立计量台账。新建台账若为合格有效记录，同样重新综合判定设备能否恢复使用
// （不直接覆盖维修侧状态；与维修完成复用同一判定）。
func (s *CalibrationService) Create(req *dto.CreateCalibrationReq, operator string) (*model.CalibrationRecord, error) {
	taken, err := s.repo.IsInstrumentNoTaken(req.InstrumentNo)
	if err != nil {
		return nil, util.NewAppError(http.StatusInternalServerError, constants.MsgInternalError, err)
	}
	if taken {
		return nil, util.NewAppError(http.StatusConflict, constants.MsgDuplicateInstrumentNo, nil)
	}
	var c *model.CalibrationRecord
	err = s.repo.DB().Transaction(func(tx *gorm.DB) error {
		// 统一加锁顺序：先锁设备行再写计量记录（与维修侧一致），并发办理时不互相覆盖。
		d, err := s.device.FindByIDForUpdate(tx, req.DeviceID)
		if errors.Is(err, repository.ErrNotFound) {
			return util.NewAppError(http.StatusNotFound, "设备不存在: device_id="+util.Uint64String(req.DeviceID), nil)
		}
		if err != nil {
			return util.NewAppError(http.StatusInternalServerError, constants.MsgInternalError, err)
		}
		if d.Status == constants.DeviceStatusScrapped {
			return util.NewAppError(http.StatusConflict, constants.MsgDeviceInScrapped, nil)
		}
		now := time.Now()
		last := req.LastCalibrationDate
		if last == nil {
			last = &now
		}
		next := last.AddDate(0, req.CalibrationCycleMonths, 0)
		c = &model.CalibrationRecord{
			InstrumentNo:           req.InstrumentNo,
			DeviceID:               d.ID,
			DeviceName:             d.Name,
			CalibrationCycleMonths: req.CalibrationCycleMonths,
			LastCalibrationDate:    last,
			NextCalibrationDate:    &next,
			Status:                 constants.CalibrationStatusNormal,
			Result:                 constants.CalibrationResultQualified,
			CertificateNo:          req.CertificateNo,
			CalibrationOrg:         req.CalibrationOrg,
			Remark:                 req.Remark,
			CreatedBy:              operator,
		}
		if err := s.repo.CreateTx(tx, c); err != nil {
			return util.NewAppError(http.StatusInternalServerError, "建立计量台账失败: instrument_no="+req.InstrumentNo, err)
		}
		// 新建合格记录后重新判定设备恢复条件。
		recheck, err := s.availability.RecheckTx(tx, c.DeviceID, RecoverTriggerCalibration)
		if err != nil {
			return err
		}
		c.RecoverNote = NoteText(recheck)
		if err := s.repo.UpdateTx(tx, c); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, wrapSvcErr(err)
	}
	s.log.Info(fmt.Sprintf(constants.LogCalibrationCreated, c.InstrumentNo, c.DeviceID, c.CalibrationCycleMonths, c.Status))
	s.audit.Record(0, operator, "CREATE", "calibration", util.Uint64String(c.ID), "建立计量台账: "+c.InstrumentNo+"；恢复判定: "+c.RecoverNote, operator, "")
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
//
// 合格：不直接把设备置为使用中，而是与维修侧复用同一综合判定——
// 只有同时不存在处理中维修工单且计量合格未过期才恢复使用，否则保持不可用并注明缺失项。
// 不合格：设备保持不可用，缺失原因合并计量不合格与（可能存在的）维修未办结，不会覆盖维修侧判断。
func (s *CalibrationService) RecordResult(id uint, req *dto.CalibrationResultReq, operator string) (*model.CalibrationRecord, error) {
	var updated *model.CalibrationRecord
	err := s.repo.DB().Transaction(func(tx *gorm.DB) error {
		// 统一加锁顺序：先设备行后计量记录（与维修侧一致），两边同时办理时后提交者基于最新状态重新判定。
		c, err := s.repo.FindByIDTx(tx, id)
		if errors.Is(err, repository.ErrNotFound) {
			return util.NewAppError(http.StatusNotFound, "计量记录不存在: id="+util.Uint64String(id), nil)
		}
		if err != nil {
			return err
		}
		d, err := s.device.FindByIDForUpdate(tx, c.DeviceID)
		if errors.Is(err, repository.ErrNotFound) {
			return util.NewAppError(http.StatusNotFound, "设备不存在: device_id="+util.Uint64String(c.DeviceID), nil)
		}
		if err != nil {
			return util.NewAppError(http.StatusInternalServerError, constants.MsgInternalError, err)
		}
		c, err = s.repo.FindByIDForUpdate(tx, id)
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
		if req.Result == constants.CalibrationResultQualified {
			c.Status = constants.CalibrationStatusNormal
			c.Result = constants.CalibrationResultQualified
		} else {
			c.Status = constants.CalibrationStatusUnqualified
			c.Result = constants.CalibrationResultUnqualified
		}
		if err := s.repo.UpdateTx(tx, c); err != nil {
			return err
		}
		// 合格/不合格都重新综合判定：合格可能因维修未办结而保持不可用，
		// 不合格也需要与维修缺失项合并说明，而不是无条件覆盖为禁用。
		if d.Status != constants.DeviceStatusScrapped {
			recheck, err := s.availability.RecheckTx(tx, c.DeviceID, RecoverTriggerCalibration)
			if err != nil {
				return err
			}
			c.RecoverNote = NoteText(recheck)
			if err := s.repo.UpdateTx(tx, c); err != nil {
				return err
			}
		}
		updated = c
		return nil
	})
	if err != nil {
		return nil, wrapSvcErr(err)
	}
	s.log.Info(fmt.Sprintf(constants.LogCalibrationResult, updated.InstrumentNo, updated.Result, updated.Status, updated.DeviceID))
	auditDetail := "登记计量结果: " + updated.InstrumentNo + "=" + updated.Result
	if updated.RecoverNote != "" {
		auditDetail += "；恢复判定: " + updated.RecoverNote
	}
	s.audit.Record(0, operator, "RESULT", "calibration", util.Uint64String(updated.ID), auditDetail, operator, "")
	return updated, nil
}
