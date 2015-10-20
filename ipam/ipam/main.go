// Copyright (c) 2015 Pani Networks
// All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

// Command-line for running IPAM

import (
    "fmt"
    "github.com/romanaproject/pani_core/ipam"
    "github.com/romanaproject/pani_core/common"
    "database/sql"
    "github.com/go-sql-driver/mysql"
)

func main() {
  fmt.Println(common.ImportantUtility())
  s, err :=  sql.Open("mysql", "user:password@/dbname")
  s.Close()	
  ns := new(mysql.NullTime)
  fmt.Println(ns)
  fmt.Println("Of course opening mysql will fail: ",err)
  fmt.Println("Hello... My address is", ipam.GetAddress)
}