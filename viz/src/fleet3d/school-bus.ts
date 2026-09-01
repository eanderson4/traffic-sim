import * as THREE from 'three'

export interface VehicleDims {
  length: number
  width: number
  height: number
}

export const dims: VehicleDims = {
  length: 11.5,
  width: 2.5,
  height: 3.1,
}

// Side profile (shape x = longitudinal, becomes world z; y = height).
// Conventional school bus: low hood ahead of a flat-front tall cabin, then a
// long flat roof — the nose is what separates it from a city bus. Floor 0.35 m.
const sideProfile = new THREE.Shape()
sideProfile.moveTo(5.75, 0.35)
sideProfile.lineTo(5.75, 1.15)
sideProfile.lineTo(5.4, 1.32)
sideProfile.lineTo(3.5, 1.42)
sideProfile.lineTo(3.5, 3.1)
sideProfile.lineTo(-5.75, 3.1)
sideProfile.lineTo(-5.75, 0.35)
sideProfile.closePath()

const bodyGeometry = new THREE.ExtrudeGeometry(sideProfile, {
  depth: 2.5,
  bevelEnabled: false,
})
bodyGeometry.rotateY(-Math.PI / 2)
bodyGeometry.translate(1.25, 0, 0)

const wheelGeo = new THREE.CylinderGeometry(0.45, 0.45, 0.3, 8)
wheelGeo.rotateZ(-Math.PI / 2)
const hubGeo = new THREE.CircleGeometry(0.2, 6)

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

  const skirt = new THREE.Mesh(new THREE.BoxGeometry(2.3, 0.35, 11.4), chassisMat)
  skirt.position.set(0, 0.175, 0)
  group.add(skirt)

  const cabin = new THREE.Group()
  cabin.name = 'cabin'

  // Tall even side glass bands (1.8–2.7 m high), five windows per side.
  const sideBandGeo = new THREE.BoxGeometry(0.08, 0.9, 7.5)
  for (const x of [-1.23, 1.23]) {
    const band = new THREE.Mesh(sideBandGeo, glassMat)
    band.position.set(x, 2.25, -0.5)
    cabin.add(band)
  }

  // Window pillars over the band — the even rhythm that reads "bus".
  const pillarGeo = new THREE.BoxGeometry(0.12, 1.0, 0.14)
  for (const x of [-1.25, 1.25]) {
    for (const z of [1.75, 0.25, -1.25, -2.75]) {
      const pillar = new THREE.Mesh(pillarGeo, bodyMat)
      pillar.position.set(x, 2.25, z)
      cabin.add(pillar)
    }
  }

  // Flat windshield against the vertical cabin front.
  const windshield = new THREE.Mesh(new THREE.BoxGeometry(2.3, 1.15, 0.08), glassMat)
  windshield.position.set(0, 2.3, 3.48)
  cabin.add(windshield)

  // Sign panel above the windshield.
  const signPanel = new THREE.Mesh(new THREE.BoxGeometry(1.4, 0.22, 0.1), chassisMat)
  signPanel.position.set(0, 2.96, 3.47)
  cabin.add(signPanel)

  group.add(cabin)

  // Folding stop-sign nub on the left flank, at the driver's window.
  const stopSign = new THREE.Mesh(new THREE.BoxGeometry(0.06, 0.34, 0.34), taillightMat)
  stopSign.position.set(-1.28, 2.05, 2.5)
  group.add(stopSign)

  const wheels = new THREE.Group()
  wheels.name = 'wheels'
  const axles = [2.5, -3.2]
  for (const z of axles) {
    for (const x of [1.14, -1.14]) {
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
  const frontGeo = new THREE.BoxGeometry(0.38, 0.22, 0.08)
  for (const x of [-0.8, 0.8]) {
    const light = new THREE.Mesh(frontGeo, headlightMat)
    light.position.set(x, 0.85, 5.72)
    lightsFront.add(light)
  }
  group.add(lightsFront)

  const lightsRear = new THREE.Group()
  lightsRear.name = 'lights-rear'
  const rearGeo = new THREE.BoxGeometry(0.32, 0.28, 0.08)
  for (const x of [-0.85, 0.85]) {
    const light = new THREE.Mesh(rearGeo, taillightMat)
    light.position.set(x, 1.0, -5.72)
    lightsRear.add(light)
  }
  group.add(lightsRear)

  return group
}
