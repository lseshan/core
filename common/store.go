// Copyright (c) 2016 Pani Networks
// All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

// Contains general routines and definitions for a generic back-end storage
// (currently geared towards RDBMS but not necessarily limited to that).
package common

import (
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"github.com/jinzhu/gorm"
	"github.com/mattn/go-sqlite3"
	log "github.com/romana/rlog"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// StoreConfig stores information needed for a DB connection.
type StoreConfig struct {
	Host     string
	Port     uint64
	Username string
	Password string
	Database string
	// Database type, e.g., sqlite3, mysql, etc.
	// TODO add a set of constants for it.
	Type string
}

const (
	MySQLUniqueConstraintErrorCode = 1062
)

// DbToHttpError produces an appropriate HttpError given an error, if it can
// (for example, producing a 409 CONFLICT in case of a unique or primary key
// constraint violation). If it cannot, it returns the original error.
func DbToHttpError(err error) error {
	switch err := err.(type) {
	case sqlite3.Error:
		if err.Code == sqlite3.ErrConstraint {
			if err.ExtendedCode == sqlite3.ErrConstraintUnique || err.ExtendedCode == sqlite3.ErrConstraintPrimaryKey {
				log.Infof("Error: %s", err)
				return HttpError{StatusCode: http.StatusConflict}
			}
		} else if err.Code == sqlite3.ErrCantOpen {
			log.Infof("Cannot open database file.")
			return NewError500("Database error.")
		}
		log.Infof("DbToHttpError(): Unknown sqlite3 error: %d|%d|%s", err.Code, err.ExtendedCode, err.Error())
		return err
	case *mysql.MySQLError:
		if err.Number == MySQLUniqueConstraintErrorCode {
			log.Infof("Error: %s", err)
			return HttpError{StatusCode: http.StatusConflict}
		}
		log.Infof("DbToHttpError(): Unknown MySQL error: %d %s", err.Number, err.Message)
		return err
	default:
		log.Infof("DbToHttpError(): Unknown error: [%T] %+v", err, err)
		return err
	}
}

func (sc StoreConfig) String() string {
	return fmt.Sprintf("Host: %s, Port: %d, Username: ****, Password: ****, Database: %s, Type: %s",
		sc.Host, sc.Port, sc.Database, sc.Type)
}

// MakeStoreConfig creates StoreConfig object from a map.
func makeStoreConfig(configMap map[string]interface{}) (StoreConfig, error) {
	storeConfig := StoreConfig{}
	storeConfig.Type = configMap["type"].(string)
	if configMap["host"] != nil {
		storeConfig.Host = configMap["host"].(string)
	}
	var err error
	if configMap["port"] != nil {
		var port uint64
		portObj := configMap["port"]
		switch portObj := portObj.(type) {
		case string:
			port, err = strconv.ParseUint(portObj, 10, 64)
			if err != nil {
				return storeConfig, errors.New(fmt.Sprintf("Error parsing port %s", portObj))
			}
		case float64:
			port = uint64(portObj)
		case int:
			port = uint64(portObj)
		default:
			return storeConfig, errors.New(fmt.Sprintf("Error parsing port %v (of type %T)", portObj, portObj))
		}
		if port != 0 {
			storeConfig.Port = port
		}
	}
	if configMap["username"] != nil {
		storeConfig.Username = configMap["username"].(string)
	}
	if configMap["password"] != nil {
		storeConfig.Password = configMap["password"].(string)
	}
	storeConfig.Database = configMap["database"].(string)
	return storeConfig, nil
}

type FindFlag string

const (
	// Flags to store.Find operation
	FindFirst      = "findFirst"
	FindLast       = "findLast"
	FindExactlyOne = "findExactlyOne"
	FindAll        = "findAll"
)

// Store defines generic store interface that can be used
// by any service for persistence.
type Store interface {
	// SetConfig sets the configuration
	SetConfig(map[string]interface{}) error
	// Connect connects to the store
	Connect() error
	// Create the schema, dropping existing one if the force flag is specified
	CreateSchema(bool) error
	// Find finds entries in the store based on the query string. The meaning of the
	// flags is as follows:
	// 1. FindFirst - the first entity (as ordered by primary key) is returned.
	// 2. FindLast - tha last entity is returned
	// 3. FindExactlyOne - it is expected that only one result is to be found --
	// multiple results will yield an errror.
	// 4. FindAll - returns all.
	// Here "entities" *must* be a pointer to an array
	// of entities to find (for example, it has to be &[]Tenant{}, not Tenant{}).
	Find(query url.Values, entities interface{}, flag FindFlag) (interface{}, error)
}

// ServiceStore interface is what each service's store needs to implement.
type ServiceStore interface {
	// Entities returns list of entities (DB tables) this store is managing.
	Entities() []interface{}
	// CreateSchemaPostProcess runs whatever required post-processing after
	// schema creation (perhaps initializing DB with some initial or sample data).
	CreateSchemaPostProcess() error
}

// createSchema is a type for functions that create database schemas.
// By defining a type we can more easily store references to functions of
// the specified signature.
type createSchema func(dbStore *DbStore, force bool) error

// DbStore is a structure storing information specific to RDBMS-based
// implementation of Store.
type DbStore struct {
	ServiceStore      ServiceStore
	Config            *StoreConfig
	Db                *gorm.DB
	createSchemaFuncs map[string]createSchema
}

// Find generically implements Find() of Store interface.
func (dbStore *DbStore) Find(query url.Values, entities interface{}, flag FindFlag) (interface{}, error) {
	queryStringFieldToDbField := make(map[string]string)
	// Since entities array exists for reflection purposes
	// we need to create a new array to put found data into.
	// Otherwise we'd be reusing the same object and race conditions
	// will result.
	ptrToArrayType := reflect.TypeOf(entities)
	arrayType := ptrToArrayType.Elem()
	newEntities := reflect.New(arrayType).Interface()
	t := reflect.TypeOf(newEntities).Elem().Elem()
	for i := 0; i < t.NumField(); i++ {
		structField := t.Field(i)
		fieldTag := structField.Tag
		fieldName := structField.Name

		queryStringField := strings.ToLower(fieldName)
		dbField := strings.ToLower(fieldName)
		if fieldTag == "" {
			// If there is no tag, then query variable is just the same as
			// the fieldName...
			log.Infof("No tag for %s", fieldName)
		} else {
			jTag := fieldTag.Get("json")
			if jTag == "" {
				log.Infof("No JSON tag for %s", fieldName)
			} else {
				jTagElts := strings.Split(jTag, ",")
				// This takes care of ",omitempty"
				if len(jTagElts) > 1 {
					queryStringField = jTagElts[0]
				} else {
					queryStringField = jTag
				}
			}
			gormTag := fieldTag.Get("gorm")
			//			log.Infof("Gorm tag for %s: %s (%v)", fieldName, gormTag, fieldTag)
			if gormTag != "" {
				// See model_struct.go:parseTagSetting
				gormVals := strings.Split(gormTag, ";")
				for _, gormVal := range gormVals {
					elts := strings.Split(gormVal, ":")
					if len(elts) == 0 {
						continue
					}
					k := strings.TrimSpace(strings.ToUpper(elts[0]))
					if k == "COLUMN" {
						if len(elts) != 2 {
							return nil, NewError400(fmt.Sprintf("Expected 2 elements in %s (in %s)", gormVal, gormTag))
						}
						dbField = elts[1]
						break
					}

				}
			}
		}
		//		log.Infof("For %s, query string field %s, struct field %s, DB field %s", t, queryStringField, fieldName, dbField)
		queryStringFieldToDbField[queryStringField] = dbField
	}
	whereMap := make(map[string]interface{})

	for k, v := range query {
		k = strings.ToLower(k)
		dbFieldName := queryStringFieldToDbField[k]
		if dbFieldName == "" {
			return nil, NewError400(fmt.Sprintf("Unknown field %s in %v", k, t))
		}
		if len(v) > 1 {
			return nil, NewError400("Did not expect multiple values in " + k)
		}
		whereMap[dbFieldName] = v[0]
	}

	//	log.Infof("Store: Querying with %+v - %T", whereMap, newEntities)

	var db *gorm.DB

	if flag == FindFirst || flag == FindLast {
		var count int
		entityPtrVal := reflect.New(reflect.TypeOf(newEntities).Elem().Elem())
		entityPtr := entityPtrVal.Interface()
		if flag == FindFirst {
			db = dbStore.Db.Where(whereMap).First(entityPtr).Count(&count)
		} else {
			db = dbStore.Db.Where(whereMap).Last(entityPtr).Count(&count)
		}
		err := GetDbErrors(db)
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, NewError404(t.String(), fmt.Sprintf("%+v", whereMap))
		}
		return entityPtr, nil
	}

	db = dbStore.Db.Where(whereMap).Find(newEntities)
	err := GetDbErrors(db)
	if err != nil {
		return nil, err
	}
	rowCount := reflect.ValueOf(newEntities).Elem().Len()

	if rowCount == 0 {
		return nil, NewError404(t.String(), fmt.Sprintf("%+v", whereMap))
	}

	if flag == FindExactlyOne {
		if rowCount == 1 {
			return reflect.ValueOf(newEntities).Elem().Index(0).Interface(), nil
		} else {
			return nil, NewError500(fmt.Sprintf("Multiple results found for %+v: %+v", query, reflect.ValueOf(newEntities).Elem().Interface()))
		}
	}

	return newEntities, nil
}

// SetConfig sets the config object from a map.
func (dbStore *DbStore) SetConfig(configMap map[string]interface{}) error {
	config, err := makeStoreConfig(configMap)
	if err != nil {
		return err
	}
	dbStore.Config = &config
	dbStore.createSchemaFuncs = make(map[string]createSchema)
	dbStore.createSchemaFuncs["mysql"] = createSchemaMysql
	dbStore.createSchemaFuncs["sqlite3"] = createSchemaSqlite3
	return nil
}

// GetPasswordFunction returns appropriate function to hash
// password depending on the underlying DB (note that in sqlite
// it is plain text).
func (dbStore *DbStore) GetPasswordFunction() (string, error) {
	switch dbStore.Config.Type {
	case "mysql":
		return "MD5(?)", nil
	case "sqlite3":
		return "?", nil
	}
	return "", errors.New(fmt.Sprintf("Unknown database: %s", dbStore.Config.Type))
}

func (dbStore *DbStore) DbStore() DbStore {
	return *dbStore
}

// connectDB gets multiple connection strings and
// tries to connect to them till it is successful.
func (dbStore *DbStore) connectDB() error {
	var errs []error
	if dbStore.Config == nil {
		return errors.New("No configuration specified.")
	}
	connStrs := dbStore.getConnStrings()
	for _, str := range connStrs {
		fmt.Println("dbStore.Config.Type: ", dbStore.Config.Type)
		fmt.Println("str: ", str)
		db, err := gorm.Open(dbStore.Config.Type, str)
		if err == nil {
			if dbStore.Config.Type == "sqlite3" {
				db.DB().SetMaxOpenConns(1)
			}
			dbStore.Db = db
			return nil
		}
		errs = append(errs, err)
	}

	var errsStr string
	for i, e := range errs {
		if i == 0 {
			errsStr = fmt.Sprintf("%s", e)
		} else {
			errsStr = fmt.Sprintf("%s\n%s", errsStr, e)
		}
	}
	return fmt.Errorf(errsStr)
}

// getConnStrings returns the appropriate GORM connection string for
// the given DB.
func (dbStore *DbStore) getConnStrings() []string {
	var connStr []string
	info := dbStore.Config
	switch info.Type {
	case "sqlite3":
		connStr = append(connStr, info.Database)
		log.Infof("DB: Connection string: %s", info.Database)
	default:
		portStr := fmt.Sprintf(":%d", info.Port)
		if info.Port == 0 {
			portStr = ":3306"
		}
		connStr = append(connStr, fmt.Sprintf("%s:%s@tcp(%s%s)/%s?parseTime=true",
			info.Username, info.Password, info.Host, portStr, info.Database))
		log.Infof("DB: Connection string: ****:****@tcp(%s%s)/%s?parseTime=true",
			info.Host, portStr, info.Database)
		connStr = append(connStr, fmt.Sprintf("%s:%s@unix(/var/run/mysqld/mysqld.sock)/%s?parseTime=true",
			info.Username, info.Password, info.Database))
		log.Infof("DB: Connection string: ****:****@unix(/var/run/mysqld/mysqld.sock))/%s?parseTime=true",
			info.Database)
		connStr = append(connStr, fmt.Sprintf("%s:%s@unix(/tmp/mysqld.sock)/%s?parseTime=true",
			info.Username, info.Password, info.Database))
		log.Infof("DB: Connection string: ****:****@unix(/tmp/mysqld.sock))/%s?parseTime=true",
			info.Database)
	}
	return connStr
}

// Connect connects to the appropriate DB (mutating dbStore's state with
// the connection information), or returns an error.
func (dbStore *DbStore) Connect() error {
	return dbStore.connectDB()
}

// CreateSchema creates the schema in this DB. If force flag
// is specified, the schema is dropped and recreated.
func (dbStore *DbStore) CreateSchema(force bool) error {
	f := dbStore.createSchemaFuncs[dbStore.Config.Type]
	if f == nil {
		return errors.New(fmt.Sprintf("Unable to create schema for %s", dbStore.Config.Type))
	}
	return f(dbStore, force)
}

// createSchemaSqlite3 creates schema for a sqlite3 db
func createSchemaSqlite3(dbStore *DbStore, force bool) error {
	schemaName := dbStore.Config.Database
	log.Infof("Entering createSchemaSqlite3() with %s", schemaName)
	var err error
	if force {
		finfo, err := os.Stat(schemaName)
		exist := finfo != nil || os.IsExist(err)
		log.Infof("Before attempting to drop %s, exists: %t, stat: [%v] ... [%v]", schemaName, exist, finfo, err)
		if exist {
			err = os.Remove(schemaName)
			if err != nil {
				return err
			}
		}
	}
	err = dbStore.Connect()
	if err != nil {
		return err
	}

	entities := dbStore.ServiceStore.Entities()
	log.Infof("Creating tables for %v", entities)
	for _, entity := range entities {
		log.Infof("sqlite3: Creating table for %T", entity)
		db := dbStore.Db.CreateTable(entity)
		if db.Error != nil {
			return db.Error
		}
	}

	errs := dbStore.Db.GetErrors()
	log.Infof("sqlite3: Errors: %v", errs)
	err2 := MakeMultiError(errs)

	if err2 != nil {
		return err2
	}
	return dbStore.ServiceStore.CreateSchemaPostProcess()
}

// createSchemaMysql creates schema for a MySQL db
func createSchemaMysql(dbStore *DbStore, force bool) error {
	log.Infof("in createSchema(%t)", force)

	schemaName := dbStore.Config.Database
	dbStore.Config.Database = "mysql"
	err := dbStore.Connect()
	if err != nil {
		return err
	}

	db := dbStore.Db
	var sql string
	if force {
		sql = fmt.Sprintf("DROP DATABASE IF EXISTS %s", schemaName)
		db.Exec(sql)
	}

	sql = fmt.Sprintf("CREATE DATABASE %s CHARACTER SET ascii COLLATE ascii_general_ci", schemaName)
	db.Exec(sql)
	err = MakeMultiError(db.GetErrors())
	if err != nil {
		return err
	}

	dbStore.Config.Database = schemaName
	err = dbStore.Connect()
	if err != nil {
		return err
	}

	entities := dbStore.ServiceStore.Entities()

	for i := range entities {
		entity := entities[i]
		db := dbStore.Db.CreateTable(entity)
		if db.Error != nil {
			return db.Error
		}
	}

	err = MakeMultiError(dbStore.Db.GetErrors())
	if err != nil {
		return err
	}
	return dbStore.ServiceStore.CreateSchemaPostProcess()
}
