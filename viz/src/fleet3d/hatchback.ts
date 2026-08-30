import * as THREE from 'three'

export interface VehicleDims {
  length: number
  width: number
  height: number
}

export const dims: VehicleDims = {
  length: 4.2,
  width: 1.8,
  height: 1.5,
}

const sideProfile = new THREE.Shape()
sideProfile.moveTo(2.1, 0.2)
sideProfile.lineTo(2.1, 0.55)
sideProfile.lineTo(1.05, 0.65)
sideProfile.lineTo(0.45, 1.4)
sideProfile.lineTo(-0.75, 1.5)
sideProfile.lineTo(-2.1, 0.6)
sideProfile.lineTo(-2.1, 0.2)
sideProfile.closePath()

const bodyGeometry = new THREE.ExtrudeGeometry(sideProfile, {
  depth: 1.8,
  bevelEnabled: false,
})
bodyGeometry.rotateY(-Math.PI / 2)
bodyGeometry.translate(0.9, 0, 0)

const wheelGeoRight = new THREE.CylinderGeometry(0.3, 0.3, 0.2, 10)
wheelGeoRight.rotateZ(-Math.PI / 2)

const wheelGeoLeft = new THREE.CylinderGeometry(0.3, 0.3, 0.2, 10)
wheelGeoLeft.rotateZ(Math.PI / 2)

const hubGeoRight = new THREE.CircleGeometry(0.12, 6)
hubGeoRight.rotateY(Math.PI / 2)

const hubGeoLeft = new THREE.CircleGeometry(0.12, 6)
hubGeoLeft.rotateY(-Math.PI / 2)

const lightGeo = new THREE.BoxGeometry(0.4, 0.12, 0.06)

function makeWheel(
  x: number,
  z: number,
  side: 1 | -1,
  wheelMat: THREE.Material,
  hubMat: THREE.Material
): THREE.Group {
  const group = new THREE.Group()

  const tire = new THREE.Mesh(side === 1 ? wheelGeoRight : wheelGeoLeft, wheelMat)
  tire.position.set(x, 0.3, z)
  group.add(tire)

  const hub = new THREE.Mesh(side === 1 ? hubGeoRight : hubGeoLeft, hubMat)
  hub.position.set(x + side * 0.1, 0.3, z)
  group.add(hub)

  return group
}

export function build(): THREE.Group {
  const bodyMat = new THREE.MeshStandardMaterial({ color: 0x5f8dff, flatShading: true })
  const cabinMat = new THREE.MeshStandardMaterial({ color: 0x1d2950, flatShading: true })
  const wheelMat = new THREE.MeshStandardMaterial({ color: 0x101018, flatShading: true })
  const hubMat = new THREE.MeshStandardMaterial({ color: 0x5c6ba8, flatShading: true })
  const chassisMat = new THREE.MeshStandardMaterial({ color: 0x0a1230, flatShading: true })
  const headlightMat = new THREE.MeshStandardMaterial({ color: 0xfff3c4, emissive: 0xfff3c4, flatShading: true })
  const taillightMat = new THREE.MeshStandardMaterial({ color: 0xe5484d, emissive: 0xe5484d, flatShading: true })

  const body = new THREE.Mesh(bodyGeometry, bodyMat)
  body.name = 'body'

  const cabin = new THREE.Mesh(new THREE.BoxGeometry(1.84, 0.38, 1.1), cabinMat)
  cabin.position.set(0, 1.09, -0.15)
  cabin.name = 'cabin'

  const wheels = new THREE.Group()
  wheels.name = 'wheels'
  wheels.add(makeWheel(0.82, 1.25, 1, wheelMat, hubMat))
  wheels.add(makeWheel(0.82, -1.25, 1, wheelMat, hubMat))
  wheels.add(makeWheel(-0.82, 1.25, -1, wheelMat, hubMat))
  wheels.add(makeWheel(-0.82, -1.25, -1, wheelMat, hubMat))

  const chassis = new THREE.Mesh(new THREE.BoxGeometry(1.5, 0.08, 3.6), chassisMat)
  chassis.position.y = 0.14

  const lightsFront = new THREE.Group()
  lightsFront.name = 'lights-front'
  for (const x of [-0.65, 0.65]) {
    const light = new THREE.Mesh(lightGeo, headlightMat)
    light.position.set(x, 0.5, 2.13)
    lightsFront.add(light)
  }

  const lightsRear = new THREE.Group()
  lightsRear.name = 'lights-rear'
  for (const x of [-0.65, 0.65]) {
    const light = new THREE.Mesh(lightGeo, taillightMat)
    light.position.set(x, 0.5, -2.13)
    lightsRear.add(light)
  }

  const root = new THREE.Group()
  root.add(body, cabin, wheels, lightsFront, lightsRear, chassis)
  return root
}
