import * as THREE from 'three'

export interface VehicleDims {
  length: number
  width: number
  height: number
}

export const dims: VehicleDims = {
  length: 12.0,
  width: 2.55,
  height: 3.1,
}

// Side profile (shape x = longitudinal, becomes world z; y = height).
// Sloped windshield front, flat roof, vertical rear. Floor at 0.35 m.
const sideProfile = new THREE.Shape()
sideProfile.moveTo(6.0, 0.35)
sideProfile.lineTo(5.7, 1.3)
sideProfile.lineTo(5.3, 3.1)
sideProfile.lineTo(-6.0, 3.1)
sideProfile.lineTo(-6.0, 0.35)
sideProfile.closePath()

const bodyGeometry = new THREE.ExtrudeGeometry(sideProfile, {
  depth: 2.55,
  bevelEnabled: false,
})
bodyGeometry.rotateY(-Math.PI / 2)
bodyGeometry.translate(1.275, 0, 0)

const wheelGeo = new THREE.CylinderGeometry(0.45, 0.45, 0.3, 10)
wheelGeo.rotateZ(-Math.PI / 2)
const hubGeo = new THREE.CircleGeometry(0.22, 8)

export function build(): THREE.Group {
  const group = new THREE.Group()

  const bodyMat = new THREE.MeshStandardMaterial({ color: '#5f8dff', flatShading: true })
  const glassMat = new THREE.MeshStandardMaterial({ color: '#1d2950', flatShading: true })
  const wheelMat = new THREE.MeshStandardMaterial({ color: '#101018', flatShading: true })
  const hubMat = new THREE.MeshStandardMaterial({ color: '#5c6ba8', flatShading: true })
  const chassisMat = new THREE.MeshStandardMaterial({ color: '#0a1230', flatShading: true })
  const headlightMat = new THREE.MeshStandardMaterial({ color: '#fff3c4', emissive: '#fff3c4', flatShading: true })
  const taillightMat = new THREE.MeshStandardMaterial({ color: '#e5484d', emissive: '#e5484d', flatShading: true })

  const body = new THREE.Mesh(bodyGeometry, bodyMat)
  body.name = 'body'
  group.add(body)

  const skirt = new THREE.Mesh(new THREE.BoxGeometry(2.35, 0.35, 11.9), chassisMat)
  skirt.position.set(0, 0.175, 0)
  group.add(skirt)

  const cabin = new THREE.Group()
  cabin.name = 'cabin'

  // Continuous side glass bands, flush with the body sides (1.1–1.9 m high).
  const sideBandGeo = new THREE.BoxGeometry(0.08, 0.8, 9.5)
  for (const x of [-1.235, 1.235]) {
    const band = new THREE.Mesh(sideBandGeo, glassMat)
    band.position.set(x, 1.5, -0.75)
    cabin.add(band)
  }

  // Windshield, approximated vertical against the sloped front profile.
  const windshield = new THREE.Mesh(new THREE.BoxGeometry(2.3, 1.3, 0.08), glassMat)
  windshield.position.set(0, 2.05, 5.44)
  cabin.add(windshield)

  // Destination box above the windshield.
  const destination = new THREE.Mesh(new THREE.BoxGeometry(1.2, 0.25, 0.1), chassisMat)
  destination.position.set(0, 2.9, 5.4)
  cabin.add(destination)

  group.add(cabin)

  const wheels = new THREE.Group()
  wheels.name = 'wheels'
  const axles = [4.4, -3.4]
  for (const z of axles) {
    for (const x of [1.17, -1.17]) {
      const wheel = new THREE.Mesh(wheelGeo, wheelMat)
      wheel.position.set(x, 0.45, z)
      wheels.add(wheel)

      const outward = x >= 0 ? 1 : -1
      const hub = new THREE.Mesh(hubGeo, hubMat)
      hub.position.set(x + outward * 0.151, 0.45, z)
      hub.rotation.y = (outward * Math.PI) / 2
      wheels.add(hub)
    }
  }
  group.add(wheels)

  const lightsFront = new THREE.Group()
  lightsFront.name = 'lights-front'
  const frontGeo = new THREE.BoxGeometry(0.4, 0.25, 0.08)
  for (const x of [-0.85, 0.85]) {
    const light = new THREE.Mesh(frontGeo, headlightMat)
    light.position.set(x, 0.8, 5.87)
    lightsFront.add(light)
  }
  group.add(lightsFront)

  const lightsRear = new THREE.Group()
  lightsRear.name = 'lights-rear'
  const rearGeo = new THREE.BoxGeometry(0.35, 0.3, 0.08)
  for (const x of [-0.9, 0.9]) {
    const light = new THREE.Mesh(rearGeo, taillightMat)
    light.position.set(x, 2.6, -5.96)
    lightsRear.add(light)
  }
  group.add(lightsRear)

  return group
}
