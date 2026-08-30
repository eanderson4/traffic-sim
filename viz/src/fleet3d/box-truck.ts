import * as THREE from 'three'

export interface VehicleDims {
  length: number
  width: number
  height: number
}

export const dims: VehicleDims = {
  length: 11.5,
  width: 2.5,
  height: 3.6,
}

export function build(): THREE.Group {
  const group = new THREE.Group()

  const bodyMat = new THREE.MeshStandardMaterial({
    color: '#5f8dff',
    flatShading: true,
  })

  const glassMat = new THREE.MeshStandardMaterial({
    color: '#1d2950',
    flatShading: true,
  })

  const wheelMat = new THREE.MeshStandardMaterial({
    color: '#101018',
    flatShading: true,
  })

  const hubMat = new THREE.MeshStandardMaterial({
    color: '#5c6ba8',
    flatShading: true,
  })

  const chassisMat = new THREE.MeshStandardMaterial({
    color: '#0a1230',
    flatShading: true,
  })

  const headlightMat = new THREE.MeshStandardMaterial({
    color: '#fff3c4',
    emissive: new THREE.Color('#fff3c4'),
    flatShading: true,
  })

  const taillightMat = new THREE.MeshStandardMaterial({
    color: '#e5484d',
    emissive: new THREE.Color('#e5484d'),
    flatShading: true,
  })

  const bodyGroup = new THREE.Group()
  bodyGroup.name = 'body'

  const cabGeo = new THREE.BoxGeometry(2.5, 2.2, 2.2)
  const cab = new THREE.Mesh(cabGeo, bodyMat)
  cab.position.set(0, 1.5, 4.65)

  const cargoGeo = new THREE.BoxGeometry(2.5, 3.2, 9.0)
  const cargo = new THREE.Mesh(cargoGeo, bodyMat)
  cargo.position.set(0, 2.0, -1.1)

  const cabinGeo = new THREE.BoxGeometry(2.5, 0.7, 0.1)
  const cabin = new THREE.Mesh(cabinGeo, glassMat)
  cabin.name = 'cabin'
  cabin.position.set(0, 2.3, 5.705)

  bodyGroup.add(cab, cargo, cabin)
  group.add(bodyGroup)

  const chassisGeo = new THREE.BoxGeometry(2.2, 0.35, 11.4)
  const chassis = new THREE.Mesh(chassisGeo, chassisMat)
  chassis.position.set(0, 0.175, 0)
  group.add(chassis)

  const wheelsGroup = new THREE.Group()
  wheelsGroup.name = 'wheels'

  const wheelGeo = new THREE.CylinderGeometry(0.45, 0.45, 0.3, 10)
  wheelGeo.rotateZ(-Math.PI / 2)

  const hubGeo = new THREE.CircleGeometry(0.22, 8)

  const axles = [4.6, -2.9, -4.1]
  for (const z of axles) {
    for (const x of [1.14, -1.14]) {
      const wheel = new THREE.Mesh(wheelGeo, wheelMat)
      wheel.position.set(x, 0.45, z)
      wheelsGroup.add(wheel)

      const outward = x >= 0 ? 1 : -1
      const hub = new THREE.Mesh(hubGeo, hubMat)
      hub.position.set(x + outward * 0.1505, 0.45, z)
      hub.rotation.y = outward * Math.PI / 2
      wheelsGroup.add(hub)
    }
  }

  group.add(wheelsGroup)

  const lightsFront = new THREE.Group()
  lightsFront.name = 'lights-front'

  const lightGeo = new THREE.BoxGeometry(0.4, 0.25, 0.1)

  const frontLightPositions = [
    { x: -0.85, y: 0.75, z: 5.72 },
    { x: 0.85, y: 0.75, z: 5.72 },
  ]

  for (const p of frontLightPositions) {
    const light = new THREE.Mesh(lightGeo, headlightMat)
    light.position.set(p.x, p.y, p.z)
    lightsFront.add(light)
  }

  group.add(lightsFront)

  const lightsRear = new THREE.Group()
  lightsRear.name = 'lights-rear'

  const rearLightPositions = [
    { x: -0.85, y: 0.75, z: -5.655 },
    { x: 0.85, y: 0.75, z: -5.655 },
  ]

  for (const p of rearLightPositions) {
    const light = new THREE.Mesh(lightGeo, taillightMat)
    light.position.set(p.x, p.y, p.z)
    lightsRear.add(light)
  }

  group.add(lightsRear)

  return group
}
