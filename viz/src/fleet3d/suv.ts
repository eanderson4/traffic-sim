import * as THREE from 'three';

export interface VehicleDims { length: number; width: number; height: number }

export const dims: VehicleDims = { length: 4.9, width: 2.0, height: 1.75 };

export function build(): THREE.Group {
  const root = new THREE.Group();

  const bodyMat = new THREE.MeshStandardMaterial({ color: '#5f8dff', flatShading: true });
  const cabinMat = new THREE.MeshStandardMaterial({ color: '#1d2950', flatShading: true });
  const wheelMat = new THREE.MeshStandardMaterial({ color: '#101018', flatShading: true });
  const hubMat = new THREE.MeshStandardMaterial({ color: '#5c6ba8', flatShading: true });
  const chassisMat = new THREE.MeshStandardMaterial({ color: '#0a1230', flatShading: true });
  const headlightMat = new THREE.MeshStandardMaterial({ color: '#fff3c4', emissive: '#fff3c4', flatShading: true });
  const taillightMat = new THREE.MeshStandardMaterial({ color: '#e5484d', emissive: '#e5484d', flatShading: true });

  const body = new THREE.Mesh(new THREE.BoxGeometry(2.0, 0.35, 4.9), bodyMat);
  body.name = 'body';
  body.position.set(0, 0.675, 0);
  root.add(body);

  const cladding = new THREE.Mesh(new THREE.BoxGeometry(2.0, 0.38, 4.7), chassisMat);
  cladding.position.set(0, 0.31, 0);
  root.add(cladding);

  const roof = new THREE.Mesh(new THREE.BoxGeometry(1.75, 0.1, 3.4), bodyMat);
  roof.position.set(0, 1.7, -0.3);
  root.add(roof);

  const cabin = new THREE.Mesh(new THREE.BoxGeometry(1.7, 0.8, 2.7), cabinMat);
  cabin.name = 'cabin';
  cabin.position.set(0, 1.25, -0.3);
  root.add(cabin);

  const wheels = new THREE.Group();
  wheels.name = 'wheels';

  const wheelGeo = new THREE.CylinderGeometry(0.37, 0.37, 0.25, 10);
  wheelGeo.rotateZ(Math.PI / 2);
  const hubGeo = new THREE.CylinderGeometry(0.2, 0.2, 0.02, 6);
  hubGeo.rotateZ(Math.PI / 2);

  const wheelPositions: Array<[number, number]> = [
    [-0.88, -1.5],
    [-0.88, 1.5],
    [0.88, -1.5],
    [0.88, 1.5]
  ];

  for (const [wx, wz] of wheelPositions) {
    const wheel = new THREE.Mesh(wheelGeo, wheelMat);
    wheel.position.set(wx, 0.37, wz);
    wheels.add(wheel);

    const hub = new THREE.Mesh(hubGeo, hubMat);
    const sideSign = wx < 0 ? -1 : 1;
    hub.position.set(wx + 0.13 * sideSign, 0.37, wz);
    wheels.add(hub);
  }

  root.add(wheels);

  const lightsFront = new THREE.Group();
  lightsFront.name = 'lights-front';
  const headlightGeo = new THREE.BoxGeometry(0.5, 0.18, 0.08);
  for (const hx of [-0.55, 0.55]) {
    const hl = new THREE.Mesh(headlightGeo, headlightMat);
    hl.position.set(hx, 0.72, 2.41);
    lightsFront.add(hl);
  }
  root.add(lightsFront);

  const lightsRear = new THREE.Group();
  lightsRear.name = 'lights-rear';
  const taillightGeo = new THREE.BoxGeometry(0.28, 0.5, 0.08);
  for (const tx of [-0.55, 0.55]) {
    const tl = new THREE.Mesh(taillightGeo, taillightMat);
    tl.position.set(tx, 0.68, -2.41);
    lightsRear.add(tl);
  }
  root.add(lightsRear);

  return root;
}
