import * as THREE from 'three'

export interface VehicleDims {
  length: number
  width: number
  height: number
}

export const dims: VehicleDims = {
  length: 10.5,
  width: 2.5,
  height: 3.3,
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

  // Footprint z: -5.25..+5.25. Crew cab 3.0 m (front, +Z), 1.2 m gap, equipment body 6.3 m.
  const bodyGroup = new THREE.Group()
  bodyGroup.name = 'body'

  const cab = new THREE.Mesh(new THREE.BoxGeometry(2.5, 2.6, 3.0), bodyMat)
  cab.position.set(0, 1.65, 3.75)

  const equipment = new THREE.Mesh(new THREE.BoxGeometry(2.5, 2.5, 6.3), bodyMat)
  equipment.position.set(0, 1.6, -2.1)

  bodyGroup.add(cab, equipment)
  group.add(bodyGroup)

  // Glass band wrapping the crew cab, slightly proud of the body sides.
  const cabin = new THREE.Mesh(new THREE.BoxGeometry(2.54, 0.7, 2.96), glassMat)
  cabin.name = 'cabin'
  cabin.position.set(0, 2.35, 3.75)
  group.add(cabin)

  const chassis = new THREE.Mesh(new THREE.BoxGeometry(2.2, 0.35, 10.4), chassisMat)
  chassis.position.set(0, 0.175, 0)
  group.add(chassis)

  // Roof ladder: thin long box on small standoffs — the defining feature.
  // Top of ladder sits at dims.height (3.3 m).
  const ladderMat = hubMat
  const ladder = new THREE.Mesh(new THREE.BoxGeometry(0.5, 0.12, 9.0), ladderMat)
  ladder.position.set(0, 3.24, -0.3)
  group.add(ladder)

  const standoffGeo = new THREE.BoxGeometry(0.1, 0.35, 0.1)
  for (const z of [3.8, -0.5, -4.0]) {
    const standoff = new THREE.Mesh(standoffGeo, ladderMat)
    standoff.position.set(0, 3.0, z)
    group.add(standoff)
  }

  const wheels = new THREE.Group()
  wheels.name = 'wheels'

  // Capped cylinders (open-ended read as floating arcs from the side) and
  // nudged outward so the treads clear the body sides.
  const wheelGeo = new THREE.CylinderGeometry(0.5, 0.5, 0.32, 8)
  wheelGeo.rotateZ(-Math.PI / 2)
  const hubGeo = new THREE.CircleGeometry(0.2, 6)

  const axles = [4.2, -2.8, -4.0]
  for (const z of axles) {
    for (const x of [1.18, -1.18]) {
      const wheel = new THREE.Mesh(wheelGeo, wheelMat)
      wheel.position.set(x, 0.5, z)
      wheels.add(wheel)

      const outward = x >= 0 ? 1 : -1
      const hub = new THREE.Mesh(hubGeo, hubMat)
      hub.position.set(x + outward * 0.161, 0.5, z)
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
    light.position.set(x, 0.8, 5.22)
    lightsFront.add(light)
  }
  group.add(lightsFront)

  const lightsRear = new THREE.Group()
  lightsRear.name = 'lights-rear'
  for (const x of [-0.85, 0.85]) {
    const light = new THREE.Mesh(lightGeo, taillightMat)
    light.position.set(x, 1.0, -5.22)
    lightsRear.add(light)
  }
  group.add(lightsRear)

  return group
}
