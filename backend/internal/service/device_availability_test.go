package service

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/medasset/medasset/internal/constants"
	"github.com/medasset/medasset/internal/dto"
	"github.com/medasset/medasset/internal/model"
	"github.com/medasset/medasset/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// availEnv 组装维修/计量联动测试所需的全部服务。
type availEnv struct {
	db      *testEnv
	device  *DeviceService
	maint   *MaintenanceService
	calib   *CalibrationService
	devRepo *repository.DeviceRepository
}

func newAvailEnv(t *testing.T) *availEnv {
	env := newTestServiceEnv(t)
	devRepo := repository.NewDeviceRepository(env.db)
	maintRepo := repository.NewMaintenanceRepository(env.db)
	calibRepo := repository.NewCalibrationRepository(env.db)
	avail := NewDeviceAvailabilityService(devRepo, maintRepo, calibRepo, env.logger)
	return &availEnv{
		db:      env,
		device:  NewDeviceService(devRepo, env.audit, env.logger),
		maint:   NewMaintenanceService(maintRepo, devRepo, avail, env.audit, env.logger),
		calib:   NewCalibrationService(calibRepo, devRepo, maintRepo, avail, env.audit, env.logger),
		devRepo: devRepo,
	}
}

func createTestDevice(t *testing.T, env *availEnv, assetCode string, calibRequired bool) *model.Device {
	t.Helper()
	d, err := env.device.Create(&dto.CreateDeviceReq{
		AssetCode: assetCode, Name: "除颤仪", Category: "生命支持", Department: "急诊科",
		CalibrationRequired: calibRequired,
	}, "admin")
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	return d
}

func startRepair(t *testing.T, env *availEnv, deviceID uint) *model.MaintenanceRecord {
	t.Helper()
	m, err := env.maint.Create(&dto.CreateMaintenanceReq{
		DeviceID: deviceID, Type: constants.MaintenanceTypeRepair,
		FaultDescription: "无法开机",
	}, "engineer")
	if err != nil {
		t.Fatalf("create repair: %v", err)
	}
	m, err = env.maint.Start(m.ID, &dto.StartMaintenanceReq{Engineer: "张工"}, "engineer")
	if err != nil {
		t.Fatalf("start repair: %v", err)
	}
	return m
}

// 场景1：无计量要求的设备，维修完成后直接恢复使用。
func TestAvailabilityRepairOnlyRecovers(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A1", false)
	m := startRepair(t, env, d.ID)

	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusUnderMaintenance {
		t.Fatalf("expected under_maintenance, got %s", d.Status)
	}

	if _, err := env.maint.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "更换主板"}, "engineer"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusInUse {
		t.Fatalf("expected in_use, got %s (note=%s)", d.Status, d.AvailabilityNote)
	}
}

// 场景2：有计量要求但缺合格记录，维修完成不能投用，台账与工单都要说明缺计量。
func TestAvailabilityRepairDoneButCalibMissing(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A2", true)
	m := startRepair(t, env, d.ID)

	updated, err := env.maint.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "维修完成"}, "engineer")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("expected unavailable, got %s", d.Status)
	}
	if d.AvailabilityNote == "" || updated.AvailabilityNote != d.AvailabilityNote {
		t.Fatalf("availability note mismatch: device=%q record=%q", d.AvailabilityNote, updated.AvailabilityNote)
	}

	// 登记合格计量后重新判定 → 使用中。
	c := createCalibration(t, env, d.ID)
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultQualified}, "engineer"); err != nil {
		t.Fatalf("record result: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusInUse {
		t.Fatalf("expected in_use after calibration, got %s (note=%s)", d.Status, d.AvailabilityNote)
	}
}

// 场景3：计量先合格但维修还在处理中，不能投用；维修完成后才使用中。
func TestAvailabilityCalibQualifiedButRepairOpen(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A3", true)
	c := createCalibration(t, env, d.ID)
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultQualified}, "engineer"); err != nil {
		t.Fatalf("record result: %v", err)
	}
	m := startRepair(t, env, d.ID)

	// 此时计量合格，但有处理中维修工单 → 保持维修中并说明。
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultQualified}, "engineer"); err != nil {
		t.Fatalf("re-record result: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusUnderMaintenance {
		t.Fatalf("expected under_maintenance, got %s", d.Status)
	}

	if _, err := env.maint.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "done"}, "engineer"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusInUse {
		t.Fatalf("expected in_use, got %s (note=%s)", d.Status, d.AvailabilityNote)
	}
}

// 场景4：计量过期视为不满足，重新计量合格后恢复。
func TestAvailabilityExpiredCalibrationBlocks(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A4", true)
	c := createCalibration(t, env, d.ID)
	past := time.Now().Add(-24 * time.Hour)
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultQualified, NextCalibrationDate: &past,
	}, "engineer"); err != nil {
		t.Fatalf("record result: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusUnavailable {
		t.Fatalf("expected unavailable for expired calib, got %s", d.Status)
	}

	future := time.Now().Add(30 * 24 * time.Hour)
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{
		Result: constants.CalibrationResultQualified, NextCalibrationDate: &future,
	}, "engineer"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusInUse {
		t.Fatalf("expected in_use, got %s", d.Status)
	}
}

// 场景5：计量不合格 → 已禁用，即使维修已闭环也不能投用。
func TestAvailabilityUnqualifiedDisables(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A5", true)
	m := startRepair(t, env, d.ID)
	if _, err := env.maint.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "done"}, "engineer"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	c := createCalibration(t, env, d.ID)
	updated, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultUnqualified}, "engineer")
	if err != nil {
		t.Fatalf("record result: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusDisabled {
		t.Fatalf("expected disabled, got %s", d.Status)
	}
	if updated.AvailabilityNote == "" || d.AvailabilityNote != updated.AvailabilityNote {
		t.Fatalf("note mismatch device=%q calib=%q", d.AvailabilityNote, updated.AvailabilityNote)
	}
}

// 场景6：取消待处理维修工单后，计量合格的设备应恢复使用。
func TestAvailabilityCancelRepairRecovers(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A6", true)
	c := createCalibration(t, env, d.ID)
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultQualified}, "engineer"); err != nil {
		t.Fatalf("record result: %v", err)
	}
	m, err := env.maint.Create(&dto.CreateMaintenanceReq{
		DeviceID: d.ID, Type: constants.MaintenanceTypeRepair, FaultDescription: "异响",
	}, "engineer")
	if err != nil {
		t.Fatalf("create repair: %v", err)
	}
	if _, err := env.maint.Cancel(m.ID, &dto.CancelMaintenanceReq{Reason: "误报"}, "engineer"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusInUse {
		t.Fatalf("expected in_use after cancel, got %s (note=%s)", d.Status, d.AvailabilityNote)
	}
}

// 场景7：不合格 + 维修未闭环，计量记录与台账要同时说明两项。
func TestAvailabilityUnqualifiedWithOpenRepairNotesBoth(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A7", true)
	c := createCalibration(t, env, d.ID)
	startRepair(t, env, d.ID)
	updated, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultUnqualified}, "engineer")
	if err != nil {
		t.Fatalf("record result: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusDisabled {
		t.Fatalf("expected disabled, got %s", d.Status)
	}
	if updated.AvailabilityNote != d.AvailabilityNote {
		t.Fatalf("note mismatch: %q vs %q", d.AvailabilityNote, updated.AvailabilityNote)
	}
}

// 场景8：不合格禁用后又有维修工单完成，设备不能被覆盖成其他不可用状态，计量重新合格后才恢复。
func TestAvailabilityDisabledNotOverwrittenByRepair(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-A8", true)
	c := createCalibration(t, env, d.ID)
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultUnqualified}, "engineer"); err != nil {
		t.Fatalf("record result: %v", err)
	}
	m := startRepair(t, env, d.ID)
	if _, err := env.maint.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "done"}, "engineer"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusDisabled {
		t.Fatalf("disabled must not be overwritten after repair, got %s", d.Status)
	}

	// 计量重新合格（无处理中维修工单）→ 恢复使用。
	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultQualified}, "engineer"); err != nil {
		t.Fatalf("re-record qualified: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusInUse {
		t.Fatalf("expected in_use after requalification, got %s", d.Status)
	}
}

func createCalibration(t *testing.T, env *availEnv, deviceID uint) *model.CalibrationRecord {
	t.Helper()
	c, err := env.calib.Create(&dto.CreateCalibrationReq{
		InstrumentNo: fmt.Sprintf("INS-%d-%d", deviceID, time.Now().UnixNano()),
		DeviceID:     deviceID, CalibrationCycleMonths: 12,
	}, "engineer")
	if err != nil {
		t.Fatalf("create calibration: %v", err)
	}
	return c
}

// newConcurrentAvailEnv 构造多连接文件型 SQLite 环境，用于验证维修/计量两侧并发办理互不覆盖。
// 生产环境为 MySQL：两个事务都遵循"先设备行 FOR UPDATE、再子记录行"的固定加锁顺序，
// 同一台设备的两侧办理由设备行锁串行化，后提交方在锁内重新 COUNT 到对方已落库的结果。
func newConcurrentAvailEnv(t *testing.T) *availEnv {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "avail.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Device{}, &model.PurchaseRequest{},
		&model.MaintenanceRecord{}, &model.CalibrationRecord{}, &model.TransferRequest{},
		&model.ScrapRequest{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.Default()
	audit := NewAuditService(repository.NewAuditRepository(db), logger)
	devRepo := repository.NewDeviceRepository(db)
	maintRepo := repository.NewMaintenanceRepository(db)
	calibRepo := repository.NewCalibrationRepository(db)
	avail := NewDeviceAvailabilityService(devRepo, maintRepo, calibRepo, logger)
	return &availEnv{
		db:      &testEnv{db: db, audit: audit, logger: logger},
		device:  NewDeviceService(devRepo, audit, logger),
		maint:   NewMaintenanceService(maintRepo, devRepo, avail, audit, logger),
		calib:   NewCalibrationService(calibRepo, devRepo, maintRepo, avail, audit, logger),
		devRepo: devRepo,
	}
}

// 场景9：维修完成与计量合格并发办理，两种交错顺序下最终都必须为使用中，且无任何一方写入被覆盖丢失。
func TestAvailabilityConcurrentRepairAndCalibration(t *testing.T) {
	for round := 0; round < 5; round++ {
		env := newConcurrentAvailEnv(t)
		d := createTestDevice(t, env, fmt.Sprintf("MA-C%d", round), true)
		c := createCalibration(t, env, d.ID)
		m := startRepair(t, env, d.ID)

		barrier := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var repairErr, calibErr error
		go func() {
			defer wg.Done()
			<-barrier
			_, repairErr = env.maint.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "并发完成"}, "engineer")
		}()
		go func() {
			defer wg.Done()
			<-barrier
			_, calibErr = env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultQualified}, "engineer")
		}()
		close(barrier)
		wg.Wait()
		if repairErr != nil || calibErr != nil {
			t.Fatalf("round %d: repairErr=%v calibErr=%v", round, repairErr, calibErr)
		}
		final, err := env.devRepo.FindByID(d.ID)
		if err != nil {
			t.Fatalf("find device: %v", err)
		}
		if final.Status != constants.DeviceStatusInUse {
			t.Fatalf("round %d: concurrent result = %s note=%q, want in_use", round, final.Status, final.AvailabilityNote)
		}
	}
}

// 场景10：计量先合格、维修后完成（串行但顺序与创建顺序相反），最终同样必须为使用中。
func TestAvailabilityCalibBeforeRepairSequence(t *testing.T) {
	env := newAvailEnv(t)
	d := createTestDevice(t, env, "MA-S1", true)
	c := createCalibration(t, env, d.ID)
	m := startRepair(t, env, d.ID)

	if _, err := env.calib.RecordResult(c.ID, &dto.CalibrationResultReq{Result: constants.CalibrationResultQualified}, "engineer"); err != nil {
		t.Fatalf("calibration: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusUnderMaintenance {
		t.Fatalf("expected under_maintenance while repair open, got %s", d.Status)
	}
	if _, err := env.maint.Complete(m.ID, &dto.CompleteMaintenanceReq{Content: "done"}, "engineer"); err != nil {
		t.Fatalf("repair: %v", err)
	}
	d, _ = env.devRepo.FindByID(d.ID)
	if d.Status != constants.DeviceStatusInUse {
		t.Fatalf("expected in_use, got %s note=%q", d.Status, d.AvailabilityNote)
	}
}
