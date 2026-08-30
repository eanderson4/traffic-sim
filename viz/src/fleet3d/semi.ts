import * as THREE from 'three'

export interface VehicleDims {
  length: number
  width: number
  height: number
}

export const dims: VehicleDims = {
  length: 13.5,
  width: 2.5,
  height: 3.8,
}

export function build(): THREE.Group {
  const group = new THREE.Group()

  const bodyMat = new THREE.MeshStandardMaterial({ color: '#5f8dff', flatShading: true })
  const glassMat = new THREE.MeshStandardMaterial({ color: '#1d2950', flatShading: true })
  const wheelMat = new THREE.MeshStandardMaterial({ color: '#101018', flatShading: true })
  const hubMat = new THREE.MeshStandardMaterial({ color: '#5c6ba8', flatShading: true })
  const chassisMat = new THREE.MeshStandardMaterial({ color: '#0a1230', flatShading: true })
  const headlightMat = new THREE.MeshStandardMaterial({ color: '#fff3c4', emissive: '#fff3c4', flatShading: true })
  const taillightMat = new THREE.MeshStandardMaterial({ color: '#e5484d', emissive: '#e5484d', flatShading: true })

  // Footprint z: -6.75..+6.75. Tractor 4.5 m (front, +Z), 0.4 m hitch gap, trailer 8.6 m.
  const bodyGroup = new THREE.Group()
  bodyGroup.name = 'body'

  const cab = new THREE.Mesh(new THREE.BoxGeometry(2.5, 2.8, 2.2), bodyMat)
  cab.position.set(0, 2.0, 5.65)

  const trailer = new THREE.Mesh(new THREE.BoxGeometry(2.5, 2.9, 8.6), bodyMat)
  trailer.position.set(0, 2.35, -2.45)

  bodyGroup.add(cab, trailer)
  group.add(bodyGroup)

  const cabin = new THREE.Mesh(new THREE.BoxGeometry(2.4, 0.7, 0.08), glassMat)
  cabin.name = 'cabin'
  cabin.position.set(0, 2.6, 6.71)
  group.add(cabin)

  const chassis = new THREE.Mesh(new THREE.BoxGeometry(2.2, 0.35, 13.4), chassisMat)
  chassis.position.set(0, 0.175, 0)
  group.add(chassis)

  const fifthWheel = new THREE.Mesh(new THREE.BoxGeometry(2.2, 0.15, 1.2), chassisMat)
  fifthWheel.position.set(0, 1.05, 3.1)
  group.add(fifthWheel)

  const landingGear = new THREE.Mesh(new THREE.BoxGeometry(2.0, 0.8, 0.3), chassisMat)
  landingGear.position.set(0, 0.5, 1.3)
  group.add(landingGear)

  const wheels = new THREE.Group()
  wheels.name = 'wheels'

  const wheelGeo = new THREE.CylinderGeometry(0.48, 0.48, 0.32, 8, 1, true)
  wheelGeo.rotateZ(-Math.PI / 2)
  const hubGeo = new THREE.CircleGeometry(0.2, 6)

  const axles = [5.9, 3.4, 2.5, -4.6, -5.7]
  for (const z of axles) {
    for (const x of [1.14, -1.14]) {
      const wheel = new THREE.Mesh(wheelGeo, wheelMat)
      wheel.position.set(x, 0.48, z)
      wheels.add(wheel)

      const outward = x >= 0 ? 1 : -1
      const hub = new THREE.Mesh(hubGeo, hubMat)
      hub.position.set(x + outward * 0.161, 0.48, z)
      hub.rotation.y = (outward * Math.PI) / 2
      wheels.add(hub)
    }
  }
  group.add(wheels)

  const lightsFront = new THREE.Group()
  lightsFront.name = 'lights-front'
  const lightGeo = new THREE.BoxGeometry(0.4, 0.25, 0.08)
  for (const x of [-0.85, 0.85]) {
    const light = new THREE.Mesh(lightGeo, headlightMat)
    light.position.set(x, 0.8, 6.7)
    lightsFront.add(light)
  }
  group.add(lightsFront)

  const lightsRear = new THREE.Group()
  lightsRear.name = 'lights-rear'
  for (const x of [-0.85, 0.85]) {
    const light = new THREE.Mesh(lightGeo, taillightMat)
    light.position.set(x, 1.1, -6.7)
    lightsRear.add(light)
  }
  group.add(lightsRear)

  return group
}
