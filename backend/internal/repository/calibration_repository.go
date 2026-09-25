package repository

import (
	"errors"
	"fmt"

	"github.com/medasset/medasset/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CalibrationRepository 计量台账仓储。
type CalibrationRepository struct {
	db *gorm.DB
}

func NewCalibrationRepository(db *gorm.DB) *CalibrationRepository {
	return &CalibrationRepository{db: db}
}

// DB 返回底层数据库句柄（供 service 层开启事务）。
func (r *CalibrationRepository) DB() *gorm.DB { return r.db }

// Create 创建计量记录。
func (r *CalibrationRepository) Create(c *model.CalibrationRecord) error {
	return r.db.Create(c).Error
}

// CreateTx 在指定事务中创建计量记录。
func (r *CalibrationRepository) CreateTx(tx *gorm.DB, c *model.CalibrationRecord) error {
	return tx.Create(c).Error
}

// FindByID 按 ID 查询。
func (r *CalibrationRepository) FindByID(id uint) (*model.CalibrationRecord, error) {
	var c model.CalibrationRecord
	err := r.db.First(&c, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &c, err
}

// FindByIDForUpdate 加锁查询。
func (r *CalibrationRepository) FindByIDForUpdate(tx *gorm.DB, id uint) (*model.CalibrationRecord, error) {
	var c model.CalibrationRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &c, err
}

// FindByIDTx 事务内普通查询（不加锁），用于在锁设备行之前获取记录的 device_id。
func (r *CalibrationRepository) FindByIDTx(tx *gorm.DB, id uint) (*model.CalibrationRecord, error) {
	var c model.CalibrationRecord
	err := tx.First(&c, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &c, err
}

// LatestByDeviceForUpdate 事务内加锁读取设备最近一次计量记录（用于恢复使用综合判定）。
func (r *CalibrationRepository) LatestByDeviceForUpdate(tx *gorm.DB, deviceID uint) (*model.CalibrationRecord, error) {
	var c model.CalibrationRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ?", deviceID).
		Order("id DESC").First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &c, err
}

// List 分页查询计量记录。
func (r *CalibrationRepository) List(page, pageSize int, deviceID uint, status string) ([]model.CalibrationRecord, int64, error) {
	var list []model.CalibrationRecord
	var total int64
	q := r.db.Model(&model.CalibrationRecord{})
	if deviceID > 0 {
		q = q.Where("device_id = ?", deviceID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// Update 更新计量记录。
func (r *CalibrationRepository) Update(c *model.CalibrationRecord) error {
	return r.db.Save(c).Error
}

// UpdateTx 在指定事务中更新更新计量记录。
func (r *CalibrationRepository) UpdateTx(tx *gorm.DB, c *model.CalibrationRecord) error {
	return tx.Save(c).Error
}

// ListDue 查询到期/即将到期/过期计量记录（计量到期预警）。
func (r *CalibrationRepository) ListDue() ([]model.CalibrationRecord, error) {
	var list []model.CalibrationRecord
	err := r.db.Where("next_calibration_date IS NOT NULL AND next_calibration_date <= DATE_ADD(NOW(), INTERVAL 30 DAY)").
		Order("next_calibration_date ASC").Find(&list).Error
	return list, err
}

// CountByStatus 按状态统计。
func (r *CalibrationRepository) CountByStatus(status string) (int64, error) {
	var n int64
	err := r.db.Model(&model.CalibrationRecord{}).Where("status = ?", status).Count(&n).Error
	return n, err
}

// IsInstrumentNoTaken 判断InstrumentNo是否已存在。
func (r *CalibrationRepository) IsInstrumentNoTaken(v string) (bool, error) {
	var n int64
	if err := r.db.Model(&model.CalibrationRecord{}).Where("instrument_no = ?", v).Count(&n).Error; err != nil {
		return false, fmt.Errorf("check instrument_no: %w", err)
	}
	return n > 0, nil
}
