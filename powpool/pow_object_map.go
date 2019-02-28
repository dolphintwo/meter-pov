package powpool

import (
	"fmt"
	"sync"

	"github.com/vechain/thor/thor"
)

// record the latest heights and powObjects
type latestHeightMarker struct {
	height  uint32
	powObjs []*powObject
}

func newLatestHeightMarker() *latestHeightMarker {
	return &latestHeightMarker{
		height:  0,
		powObjs: make([]*powObject, 0),
	}
}

func (mkr *latestHeightMarker) update(powObj *powObject) {
	newHeight := powObj.Height()
	if newHeight > mkr.height {
		mkr.height = newHeight
		mkr.powObjs = []*powObject{powObj}
	} else if newHeight == mkr.height {
		mkr.powObjs = append(mkr.powObjs, powObj)
	} else {
		// do nothing if newHeight < mkr.height
	}
}

// powObjectMap to maintain mapping of ID to tx object, and account quota.
type powObjectMap struct {
	lock             sync.RWMutex
	latestHeightMkr  *latestHeightMarker
	lastKframePowObj *powObject //last kblock of powObject
	powObjMap        map[thor.Bytes32]*powObject
}

func newPowObjectMap() *powObjectMap {
	return &powObjectMap{
		powObjMap:        make(map[thor.Bytes32]*powObject),
		latestHeightMkr:  newLatestHeightMarker(),
		lastKframePowObj: nil,
	}
}

func (m *powObjectMap) _add(powObj *powObject) error {
	m.lock.Lock()
	defer m.lock.Unlock()

	powID := powObj.HashID()
	if _, found := m.powObjMap[powID]; found {
		return nil
	}

	m.powObjMap[powID] = powObj
	m.latestHeightMkr.update(powObj)
	return nil
}

func (m *powObjectMap) isKframeInitialAdded() bool {
	return (m.lastKframePowObj != nil)
}

func (m *powObjectMap) Contains(powID thor.Bytes32) bool {
	m.lock.RLock()
	defer m.lock.RUnlock()

	_, found := m.powObjMap[powID]
	return found
}

func (m *powObjectMap) InitialAddKframe(powObj *powObject) error {
	if powObj.blockInfo.Version != powKframeBlockVersion {
		log.Error("InitialAddKframe: Invalid version", "verson", powObj.blockInfo.Version)
		return fmt.Errorf("InitialAddKframe: Invalid version (%v)", powObj.blockInfo.Version)
	}

	if m.isKframeInitialAdded() {
		return fmt.Errorf("Kframe already added, flush object map first")
	}

	err := m._add(powObj)
	if err != nil {
		return err
	}

	m.lastKframePowObj = powObj
	return nil
}

func (m *powObjectMap) Add(powObj *powObject) error {
	if !m.isKframeInitialAdded() {
		return fmt.Errorf("Kframe is not added")
	}

	if m.Contains(powObj.HashID()) {
		return nil
	}
	// object with previous hash MUST be in this pool
	previous := m.Get(powObj.blockInfo.HashPrevBlock)
	if previous == nil {
		return fmt.Errorf("HashPrevBlock")
	}

	err := m._add(powObj)
	return err
}

func (m *powObjectMap) Remove(powID thor.Bytes32) bool {
	m.lock.Lock()
	defer m.lock.Unlock()

	if _, ok := m.powObjMap[powID]; ok {
		delete(m.powObjMap, powID)
		return true
	}
	return false
}

func (m *powObjectMap) Get(powID thor.Bytes32) *powObject {
	m.lock.RLock()
	defer m.lock.RUnlock()

	if powObj, found := m.powObjMap[powID]; found {
		return powObj
	}
	return nil
}

func (m *powObjectMap) GetLatestObjects() []*powObject {
	m.lock.RLock()
	defer m.lock.RUnlock()

	return m.latestHeightMkr.powObjs
}

func (m *powObjectMap) Flush() {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.powObjMap = make(map[thor.Bytes32]*powObject)
	m.latestHeightMkr = newLatestHeightMarker()
	m.lastKframePowObj = nil
}

func (m *powObjectMap) Len() int {
	m.lock.RLock()
	defer m.lock.RUnlock()
	return len(m.powObjMap)
}