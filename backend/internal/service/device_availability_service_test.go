package service

import (
	"strings"
	"testing"
	"time"

	"github.com/medasset/medasset/internal/constants"
	"github.com/medasset/medasset/internal/dto"
	"github.com/medasset/medasset/internal/model"
	"github.com/medasset/medasset/internal/repository"
	"gorm.io/gorm"
)

// newAvailabilityEnv 组装维修/计量/可用性判定所需的全部 service。
func newAvailabilityEnv(t *testing.T) (*testEnv, *DeviceAvailabilityService, *MaintenanceService, *CalibrationService) {
	env := newTestServiceEnv(t)
	deviceRepo := repository.NewDeviceRepository(env.db)
	maintenanceRepo := repository.NewMaintenanceRepository(env.db)
	calibrationRepo := repository.NewCalibrationRepository(env.db)
	availability := NewDeviceAvailabilityService(deviceRepo, maintenanceRepo, calibrationRepo, env.logger)
	maintenanceSvc := NewMaintenanceService(maintenanceRepo, deviceRepo, availability, env.audit, env.logger)
	calibrationSvc := NewCalibrationService(calibrationRepo, deviceRepo, availability, env.audit, env.logger)
	return env, availability, maintenanceSvc, calibrationSvc
}

func createDeviceForRecover(t *testing.T, db *gorm.DB, calibrationRequired bool) *model.Device {
	d := &model.Device{
		AssetCode:           "RC-" + time.Now().Format("150405.000000"),
		Name:                "除颤仪",
		Status:              constants.DeviceStatusInUse,
		CalibrationRequired: calibrationRequired,
	}
	if err := db.Create(d).Error; err != nil {
		t.Fatalf("create device: %v", err)
	}
	return d
}

// 无计量要求的设备：维修完成后即恢复使用。
func TestRecover_NoCalibrationRequired_RepairCompleteRestores(t *testing.T) {
	env, _, maintSvc, _ := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, false)

	m := &model.MaintenanceRecord{
		RecordNo: "MT-R-1", DeviceID: d.ID, DeviceName: d.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusInProgress,
	}
	if err := env.db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Model(&model.Device{}).Where("id = ?", d.ID).
		Update("status", constants.DeviceStatusUnderMaintenance).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := maintSvc.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "维修完成"}, "eng"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusInUse {
		t.Fatalf("status = %q, want in_use", got.Status)
	}
	if got.UnavailableReason != "" {
		t.Errorf("reason = %q, want empty", got.UnavailableReason)
	}
}

// 有计量要求但从未计量：维修完成后仍不可用，台账与工单均注明缺计量。
func TestRecover_CalibrationRequired_MissingRecord_StaysUnavailable(t *testing.T) {
	env, _, maintSvc, _ := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, true)

	m := &model.MaintenanceRecord{
		RecordNo: "MT-R-2", DeviceID: d.ID, DeviceName: d.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusInProgress,
	}
	env.db.Create(m)
	env.db.Model(&model.Device{}).Where("id = ?", d.ID).Update("status", constants.DeviceStatusUnderMaintenance)

	updated, err := maintSvc.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "ok"}, "eng")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", got.Status)
	}
	if got.UnavailableReason != constants.RecoverBlockCalibRequired {
		t.Errorf("device reason = %q", got.UnavailableReason)
	}
	if updated.RecoverNote == "" {
		t.Error("maintenance record recover_note should not be empty")
	}
}

// 计量合格但已过期：设备保持不可用，注明计量过期。
func TestRecover_QualifiedButExpired_StaysUnavailable(t *testing.T) {
	env, _, maintSvc, _ := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, true)

	past := time.Now().Add(-2 * 24 * time.Hour)
	c := &model.CalibrationRecord{
		InstrumentNo: "INST-EXP", DeviceID: d.ID, DeviceName: d.Name,
		CalibrationCycleMonths: 1, LastCalibrationDate: &past, NextCalibrationDate: &past,
		Status: constants.CalibrationStatusExpired, Result: constants.CalibrationResultQualified,
	}
	if err := calibSvcCreateRaw(env.db, c); err != nil {
		t.Fatal(err)
	}
	m := &model.MaintenanceRecord{
		RecordNo: "MT-R-3", DeviceID: d.ID, DeviceName: d.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusInProgress,
	}
	env.db.Create(m)
	env.db.Model(&model.Device{}).Where("id = ?", d.ID).Update("status", constants.DeviceStatusUnderMaintenance)

	if _, err := maintSvc.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "ok"}, "eng"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", got.Status)
	}
	if got.UnavailableReason != constants.RecoverBlockCalibExpired {
		t.Errorf("reason = %q, want %q", got.UnavailableReason, constants.RecoverBlockCalibExpired)
	}
}

// 计量合格且未过期，同时无未办结维修：登记计量结果即恢复使用。
func TestRecover_QualifiedResult_Restores(t *testing.T) {
	env, _, _, calibSvc := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, true)

	c := &model.CalibrationRecord{
		InstrumentNo: "INST-OK", DeviceID: d.ID, DeviceName: d.Name,
		CalibrationCycleMonths: 12, Status: constants.CalibrationStatusExpired, Result: constants.CalibrationResultUnqualified,
	}
	env.db.Create(c)
	env.db.Model(&model.Device{}).Where("id = ?", d.ID).Update("status", constants.DeviceStatusDisabled)

	future := time.Now().AddDate(0, 12, 0)
	updated, err := calibSvc.RecordResult(c.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultQualified, NextCalibrationDate: &future,
	}, "qa")
	if err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusInUse {
		t.Fatalf("status = %q, want in_use", got.Status)
	}
	if updated.RecoverNote != constants.RecoverReady {
		t.Errorf("recover_note = %q", updated.RecoverNote)
	}
}

// 计量合格但仍有处理中维修工单：计量侧不能把设备抢回使用中。
func TestRecover_QualifiedButRepairOpen_StaysUnavailable(t *testing.T) {
	env, _, _, calibSvc := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, true)

	open := &model.MaintenanceRecord{
		RecordNo: "MT-OPEN", DeviceID: d.ID, DeviceName: d.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusInProgress,
	}
	env.db.Create(open)
	c := &model.CalibrationRecord{
		InstrumentNo: "INST-OPEN", DeviceID: d.ID, DeviceName: d.Name,
		CalibrationCycleMonths: 12, Status: constants.CalibrationStatusUnqualified,
		Result: constants.CalibrationResultUnqualified,
	}
	env.db.Create(c)
	env.db.Model(&model.Device{}).Where("id = ?", d.ID).Update("status", constants.DeviceStatusUnderMaintenance)

	future := time.Now().AddDate(0, 6, 0)
	if _, err := calibSvc.RecordResult(c.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultQualified, NextCalibrationDate: &future,
	}, "qa"); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", got.Status)
	}
	if got.UnavailableReason != constants.RecoverBlockRepair {
		t.Errorf("reason = %q, want repair blocked only", got.UnavailableReason)
	}
}

// 计量不合格且维修未办结：两项缺失同时注明，计量侧不覆盖维修事实。
func TestRecover_UnqualifiedAndRepairOpen_BothReasons(t *testing.T) {
	env, _, _, calibSvc := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, true)

	open := &model.MaintenanceRecord{
		RecordNo: "MT-OPEN2", DeviceID: d.ID, DeviceName: d.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusPending,
	}
	env.db.Create(open)
	c := &model.CalibrationRecord{
		InstrumentNo: "INST-BOTH", DeviceID: d.ID, DeviceName: d.Name,
		CalibrationCycleMonths: 12, Result: constants.CalibrationResultQualified,
		NextCalibrationDate: ptrTime(time.Now().AddDate(0, 6, 0)),
	}
	env.db.Create(c)

	if _, err := calibSvc.RecordResult(c.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultUnqualified,
	}, "qa"); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", got.Status)
	}
	if !strings.Contains(got.UnavailableReason, constants.RecoverBlockRepair) ||
		!strings.Contains(got.UnavailableReason, constants.RecoverBlockCalibUnqualified) {
		t.Errorf("reason = %q, want both repair and unqualified blocks", got.UnavailableReason)
	}
}

// 计量不合格 -> 不可用；随后维修工单完成时重新判定，仍因计量不合格保持不可用。
func TestRecover_MaintenanceCompleteRechecksCalibration(t *testing.T) {
	env, _, maintSvc, calibSvc := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, true)

	c := &model.CalibrationRecord{
		InstrumentNo: "INST-RC", DeviceID: d.ID, DeviceName: d.Name,
		CalibrationCycleMonths: 12, Result: constants.CalibrationResultQualified,
		NextCalibrationDate: ptrTime(time.Now().AddDate(0, 6, 0)), Status: constants.CalibrationStatusNormal,
	}
	env.db.Create(c)
	m := &model.MaintenanceRecord{
		RecordNo: "MT-RC", DeviceID: d.ID, DeviceName: d.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusInProgress,
	}
	env.db.Create(m)
	env.db.Model(&model.Device{}).Where("id = ?", d.ID).Update("status", constants.DeviceStatusUnderMaintenance)

	// 计量先登记不合格。
	if _, err := calibSvc.RecordResult(c.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultUnqualified,
	}, "qa"); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	// 维修随后完成：重新判定，应仍不可用且注明计量不合格（而不是被维修侧直接置为使用中）。
	if _, err := maintSvc.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "done"}, "eng"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", got.Status)
	}
	if got.UnavailableReason != constants.RecoverBlockCalibUnqualified {
		t.Errorf("reason = %q, want unqualified", got.UnavailableReason)
	}
}

// 并发：维修完成与计量合格同时提交，两边都完成后设备最终必须为使用中，且不会被后到者覆盖回不可用。
func TestRecover_ConcurrentCompletion_FinalInUse(t *testing.T) {
	env, availability, maintSvc, calibSvc := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, true)

	c := &model.CalibrationRecord{
		InstrumentNo: "INST-CONC", DeviceID: d.ID, DeviceName: d.Name,
		CalibrationCycleMonths: 12, Result: constants.CalibrationResultUnqualified,
	}
	env.db.Create(c)
	m := &model.MaintenanceRecord{
		RecordNo: "MT-CONC", DeviceID: d.ID, DeviceName: d.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusInProgress,
	}
	env.db.Create(m)
	env.db.Model(&model.Device{}).Where("id = ?", d.ID).Update("status", constants.DeviceStatusUnderMaintenance)

	future := time.Now().AddDate(0, 6, 0)
	errRepair := env.db.Transaction(func(tx *gorm.DB) error {
		// 模拟维修完成事务：先锁设备，完工后走共享判定。
		if _, err := availability.device.FindByIDForUpdate(tx, d.ID); err != nil {
			return err
		}
		if err := tx.Model(&model.MaintenanceRecord{}).Where("id = ?", m.ID).
			Updates(map[string]any{"status": constants.MaintenanceStatusCompleted, "executed_date": time.Now()}).Error; err != nil {
			return err
		}
		r, err := availability.RecheckTx(tx, d.ID, RecoverTriggerMaintenance)
		if err != nil {
			return err
		}
		if r.Ready {
			t.Errorf("repair tx should not see qualified calibration yet")
		}
		return nil
	})
	if errRepair != nil {
		t.Fatalf("repair tx: %v", errRepair)
	}

	// 计量后合格（此时维修已办结）：重新判定应恢复使用。
	if _, err := calibSvc.RecordResult(c.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultQualified, NextCalibrationDate: &future,
	}, "qa"); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusInUse {
		t.Fatalf("final status = %q, want in_use", got.Status)
	}

	// 反向顺序：再来一次完整流程，维修在计量合格之后完成，最终同样必须为使用中。
	d2 := createDeviceForRecover(t, env.db, true)
	c2 := &model.CalibrationRecord{
		InstrumentNo: "INST-CONC2", DeviceID: d2.ID, DeviceName: d2.Name,
		CalibrationCycleMonths: 12, Result: constants.CalibrationResultUnqualified,
	}
	env.db.Create(c2)
	m2 := &model.MaintenanceRecord{
		RecordNo: "MT-CONC2", DeviceID: d2.ID, DeviceName: d2.Name,
		Type: constants.MaintenanceTypeRepair, Status: constants.MaintenanceStatusInProgress,
	}
	env.db.Create(m2)
	env.db.Model(&model.Device{}).Where("id = ?", d2.ID).Update("status", constants.DeviceStatusUnderMaintenance)

	if _, err := calibSvc.RecordResult(c2.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultQualified, NextCalibrationDate: &future,
	}, "qa"); err != nil {
		t.Fatalf("RecordResult c2: %v", err)
	}
	if reloadDevice(t, env.db, d2.ID).Status != constants.DeviceStatusUnavailable {
		t.Fatalf("after calibration only, status should stay unavailable (repair open)")
	}
	if _, err := maintSvc.Complete(m2.ID, &dto.CompleteMaintenanceReq{Content: "done"}, "eng"); err != nil {
		t.Fatalf("Complete m2: %v", err)
	}
	if reloadDevice(t, env.db, d2.ID).Status != constants.DeviceStatusInUse {
		t.Fatalf("after both complete, status should be in_use")
	}
}

func reloadDevice(t *testing.T, db *gorm.DB, id uint) *model.Device {
	t.Helper()
	var d model.Device
	if err := db.First(&d, id).Error; err != nil {
		t.Fatalf("reload device: %v", err)
	}
	return &d
}

func calibSvcCreateRaw(db *gorm.DB, c *model.CalibrationRecord) error {
	return db.Create(c).Error
}

func ptrTime(t time.Time) *time.Time { return &t }

// 故障报修单创建即视为“处理中”，使用中的设备应立即不可用；取消后重新判定恢复使用。
func TestRecover_CreateRepairBlocksThenCancelRestores(t *testing.T) {
	env, _, maintSvc, _ := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, false)

	created, err := maintSvc.Create(&dto.CreateMaintenanceReq{
		DeviceID: d.ID, Type: constants.MaintenanceTypeRepair, FaultDescription: "开不了机",
	}, "dept")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("after repair created status = %q, want unavailable", got.Status)
	}
	if got.UnavailableReason != constants.RecoverBlockRepair {
		t.Errorf("reason = %q, want repair blocked", got.UnavailableReason)
	}
	if created.RecoverNote == "" {
		t.Error("repair order recover_note should be set at creation")
	}

	// 待处理工单可取消：取消后未办结工单归零，无计量要求设备恢复使用。
	cancelled, err := maintSvc.Cancel(created.ID, &dto.CancelMaintenanceReq{Reason: "误报"}, "admin")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got = reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusInUse {
		t.Fatalf("after cancel status = %q, want in_use", got.Status)
	}
	if cancelled.RecoverNote != constants.RecoverReady {
		t.Errorf("recover_note = %q, want ready note", cancelled.RecoverNote)
	}
}

// 多张维修工单：完成一张但仍有另一张待处理时，设备不能恢复使用。
func TestRecover_MultipleOpenRepairs_StaysUnavailable(t *testing.T) {
	env, _, maintSvc, _ := newAvailabilityEnv(t)
	d := createDeviceForRecover(t, env.db, false)

	m1, err := maintSvc.Create(&dto.CreateMaintenanceReq{DeviceID: d.ID, Type: constants.MaintenanceTypeRepair}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintSvc.Create(&dto.CreateMaintenanceReq{DeviceID: d.ID, Type: constants.MaintenanceTypeRepair}, "u"); err != nil {
		t.Fatal(err)
	}
	// 开始并完成第一张（第二张仍为 pending）。
	if _, err := maintSvc.Start(m1.ID, &dto.StartMaintenanceReq{Engineer: "eng"}, "eng"); err != nil {
		t.Fatal(err)
	}
	if _, err := maintSvc.Complete(m1.ID, &dto.CompleteMaintenanceReq{Content: "done"}, "eng"); err != nil {
		t.Fatal(err)
	}
	got := reloadDevice(t, env.db, d.ID)
	if got.Status != constants.DeviceStatusUnavailable || got.UnavailableReason != constants.RecoverBlockRepair {
		t.Fatalf("status=%q reason=%q, want unavailable/repair", got.Status, got.UnavailableReason)
	}
}
