import * as THREE from 'three'

export interface VehicleDims {
  length: number
  width: number
  height: number
}

export const dims: VehicleDims = {
  length: 8.5,
  width: 2.4,
  height: 3.1,
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

  // Walk-through box: full width, floor at 0.4 m, flat vertical front face.
  const bodyGeo = new THREE.BoxGeometry(2.4, 2.7, 8.4)
  const body = new THREE.Mesh(bodyGeo, bodyMat)
  body.name = 'body'
  body.position.set(0, 1.75, -0.05)
  group.add(body)

  // Dark front bumper / step; its face is the forward-most point.
  const bumperGeo = new THREE.BoxGeometry(2.4, 0.5, 0.1)
  const bumper = new THREE.Mesh(bumperGeo, chassisMat)
  bumper.position.set(0, 0.55, 4.2)
  group.add(bumper)

  const chassisGeo = new THREE.BoxGeometry(2.2, 0.35, 8.4)
  const chassis = new THREE.Mesh(chassisGeo, chassisMat)
  chassis.position.set(0, 0.175, -0.05)
  group.add(chassis)

  // Glass: windshield band across the flat front + cab door windows.
  const cabin = new THREE.Group()
  cabin.name = 'cabin'

  const windshieldGeo = new THREE.BoxGeometry(2.3, 0.9, 0.08)
  const windshield = new THREE.Mesh(windshieldGeo, glassMat)
  windshield.position.set(0, 2.2, 4.16)
  cabin.add(windshield)

  // Side glass proud of the body sides — flush (±1.17) left the outer face
  // coplanar with the body at ±1.20 and z-fought at grazing angles.
  const sideGlassGeo = new THREE.BoxGeometry(0.06, 0.75, 1.7)
  for (const x of [-1.21, 1.21]) {
    const side = new THREE.Mesh(sideGlassGeo, glassMat)
    side.position.set(x, 2.15, 3.0)
    cabin.add(side)
  }

  group.add(cabin)

  // Wheels: cab-over front axle close behind the bumper, single rear axle.
  const wheelsGroup = new THREE.Group()
  wheelsGroup.name = 'wheels'

  const wheelGeo = new THREE.CylinderGeometry(0.4, 0.4, 0.3, 10)
  wheelGeo.rotateZ(-Math.PI / 2)

  const hubGeo = new THREE.CircleGeometry(0.2, 8)

  const axles = [2.9, -2.7]
  for (const z of axles) {
    for (const x of [1.1, -1.1]) {
      const wheel = new THREE.Mesh(wheelGeo, wheelMat)
      wheel.position.set(x, 0.4, z)
      wheelsGroup.add(wheel)

      const outward = x >= 0 ? 1 : -1
      const hub = new THREE.Mesh(hubGeo, hubMat)
      hub.position.set(x + outward * 0.1505, 0.4, z)
      hub.rotation.y = outward * Math.PI / 2
      wheelsGroup.add(hub)
    }
  }

  group.add(wheelsGroup)

  const lightsFront = new THREE.Group()
  lightsFront.name = 'lights-front'

  const lightGeo = new THREE.BoxGeometry(0.35, 0.2, 0.06)

  for (const x of [-0.8, 0.8]) {
    const light = new THREE.Mesh(lightGeo, headlightMat)
    light.position.set(x, 1.05, 4.16)
    lightsFront.add(light)
  }

  group.add(lightsFront)

  const lightsRear = new THREE.Group()
  lightsRear.name = 'lights-rear'

  for (const x of [-0.8, 0.8]) {
    const light = new THREE.Mesh(lightGeo, taillightMat)
    light.position.set(x, 1.05, -4.22)
    lightsRear.add(light)
  }

  group.add(lightsRear)

  return group
}
