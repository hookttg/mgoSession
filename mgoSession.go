// Copyright 2013 Beego Authors
// Copyright 2014 The Macaron Authors
// Copyright 2016 HOOKTTG :),thanks for them
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package session

import (
	"fmt"
	"log"
	"sync"
	"time"
	"strings"

	"github.com/go-macaron/session"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type sessionDoc struct {
	Key    string
	Data   []byte
	Expiry int64
}

// MongodbStore represents a mongodb session store implementation.
type MongodbStore struct {
	c    *mgo.Collection
	sid  string
	lock sync.RWMutex
	data map[interface{}]interface{}
}

// NewMongodbStore creates and returns a mongodb session store.
func NewMongodbStore(c *mgo.Collection, sid string, kv map[interface{}]interface{}) *MongodbStore {
	return &MongodbStore{
		c:    c,
		sid:  sid,
		data: kv,
	}
}

// Set sets value to given key in session.
func (s *MongodbStore) Set(key, val interface{}) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.data[key] = val
	return nil
}

// Get gets value by given key in session.
func (s *MongodbStore) Get(key interface{}) interface{} {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return s.data[key]
}

// Delete delete a key from session.
func (s *MongodbStore) Delete(key interface{}) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.data, key)
	return nil
}

// ID returns current session ID.
func (s *MongodbStore) ID() string {
	return s.sid
}

// Release releases resource and save data to provider.
func (s *MongodbStore) Release() error {
	data, err := session.EncodeGob(s.data)
	if err != nil {
		return err
	}

	err = s.c.Update(bson.M{"key": s.sid},bson.M{"$set":bson.M{"data": data, "expiry": time.Now().Unix()}})
	//Fixed:if the cookie's sid not in database,Macaron will panic;so add this code
	if (err != nil) && (err == mgo.ErrNotFound) {
		err = nil
	}
	return err
}

// Flush deletes all session data.
func (s *MongodbStore) Flush() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.data = make(map[interface{}]interface{})
	return nil
}

// MongodbProvider represents a mongodb session provider implementation.
type MongodbProvider struct {
	c        *mgo.Collection
	session  *mgo.Session
	expire   int64
}

// Init initializes mongodb session provider.
// connStr: [mongodb://][user:pass@]host1[:port1][,host2[:port2],...][/database][?options]
func (p *MongodbProvider) Init(expire int64, connStr string) (err error) {
	//Fixed:if connStr is "mongodb://username:password@localhost/myDataBase",call mgo.Dial() would panic.
	//so delete myDataBase from connStr,then call mgo.Dial();next call session.DB().
	var db string
	i := strings.LastIndex(connStr, "?")
	if i > 0 {
		connStr = connStr[:i-1]
	}
	i = strings.LastIndex(connStr, "/")
	if i > 0 {
		if strings.HasPrefix(connStr, "mongodb://") {
			if i > len("mongodb://") {
				db = connStr[i+1:]
				connStr = connStr[:i]
			}
		}
	}
	//
	p.expire = expire
	p.session, err = mgo.Dial(connStr)
	if err != nil {
		return err
	}
	if db == "" {
		var dbname []string
		dbname, err = p.session.DatabaseNames()
		if (len(dbname) == 0) && err != nil {
			panic("Need database name")
		}
		db = dbname[0]
	}
	p.c = p.session.DB(db).C("session")
	return p.session.Ping()
}

// Read returns raw session store by session ID.
func (p *MongodbProvider) Read(sid string) (session.RawStore, error) {
	result := sessionDoc{}
	err := p.c.Find(bson.M{"key": sid}).One(&result)
	if err != nil {
		err = p.c.Insert(&sessionDoc{Key: sid, Expiry: time.Now().Unix()})
	}
	if err != nil {
		return nil, err
	}
	var kv map[interface{}]interface{}
	if len(result.Data) == 0 {
		kv = make(map[interface{}]interface{})
	} else {
		kv, err = session.DecodeGob(result.Data)
		if err != nil {
			return nil, err
		}
	}

	return NewMongodbStore(p.c, sid, kv), nil
}

// Exist returns true if session with given ID exists.
func (p *MongodbProvider) Exist(sid string) bool {
	num, err := p.c.Find(bson.M{"key": sid}).Count()
	if err != nil {
		panic("session/mongodb: error checking existence: " + err.Error())
	}
	return num != 0
}

// Destory deletes a session by session ID.
func (p *MongodbProvider) Destory(sid string) error {
	err := p.c.Remove(bson.M{"key": sid})
	return err
}

// Regenerate regenerates a session store from old session ID to new one.
func (p *MongodbProvider) Regenerate(oldsid, sid string) (_ session.RawStore, err error) {
	if p.Exist(sid) {
		return nil, fmt.Errorf("new sid '%s' already exists", sid)
	}
	if !p.Exist(oldsid) {
		err = p.c.Insert(&sessionDoc{Key: sid, Expiry: time.Now().Unix()})
		if err != nil {
			return nil, err
		}
	} else {
		err = p.c.Update(bson.M{"key": oldsid},bson.M{"$set":bson.M{"key": sid,"expiry": time.Now().Unix()}})
		if err != nil {
			return nil, err
		}
	}

	return p.Read(sid)
}

// Count counts and returns number of sessions.
func (p *MongodbProvider) Count() (total int) {
	var err error
	total, err = p.c.Count()
	if err != nil {
		panic("session/mongodb: error counting records: " + err.Error())
	}
	return total
}

// GC calls GC to clean expired sessions.
func (p *MongodbProvider) GC() {
	diff := time.Now().Unix() - p.expire
	_, err := p.c.RemoveAll(bson.M{"expiry": bson.M{"$lt": diff}})
	if err != nil {
		log.Printf("session/mongodb: error garbage collecting: %v", err)
	}
}

func init() {
	session.Register("mongodb", &MongodbProvider{})
}
