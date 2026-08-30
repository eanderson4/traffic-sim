import * as THREE from 'three';

export interface VehicleDims {
  length: number;
  width: number;
  height: number;
}

export const dims: VehicleDims = {
  length: 4.8,
  width: 1.9,
  height: 1.45,
};

const BODY_WIDTH = 1.72;
const CABIN_WIDTH = 1.56;
const WHEEL_X = 0.84;
const WHEEL_Z = 1.45;
const WHEEL_RADIUS = 0.32;
const WHEEL_WIDTH = 0.22;
const HUB_RADIUS = 0.16;
const HUB_THICKNESS = 0.02;

export function build(): THREE.Group {
  const root = new THREE.Group();

  const bodyMaterial = new THREE.MeshStandardMaterial({
    color: 0x5f8dff,
    flatShading: true,
  });

  const cabinMaterial = new THREE.MeshStandardMaterial({
    color: 0x1d2950,
    flatShading: true,
    side: THREE.DoubleSide,
  });

  const chassisMaterial = new THREE.MeshStandardMaterial({
    color: 0x0a1230,
    flatShading: true,
  });

  const wheelMaterial = new THREE.MeshStandardMaterial({
    color: 0x101018,
    flatShading: true,
  });

  const hubMaterial = new THREE.MeshStandardMaterial({
    color: 0x5c6ba8,
    flatShading: true,
  });

  const headlightMaterial = new THREE.MeshStandardMaterial({
    color: 0xfff3c4,
    emissive: 0xfff3c4,
    flatShading: true,
  });

  const tailLightMaterial = new THREE.MeshStandardMaterial({
    color: 0xe5484d,
    emissive: 0xe5484d,
    flatShading: true,
  });

  // Chassis / underbody slab.
  const chassisGeo = new THREE.BoxGeometry(1.5, 0.16, 4.7);
  const chassis = new THREE.Mesh(chassisGeo, chassisMaterial);
  chassis.position.y = 0.18;
  chassis.name = 'chassis';
  root.add(chassis);

  // Main lower body. Top sits at 0.62 m.
  const bodyGeo = new THREE.BoxGeometry(BODY_WIDTH, 0.36, dims.length);
  const body = new THREE.Mesh(bodyGeo, bodyMaterial);
  body.position.y = 0.26 + 0.18;
  body.name = 'body';
  root.add(body);

  // Cabin / glasshouse: extruded side profile, slightly narrower than body.
  const cabinShape = new THREE.Shape();
  cabinShape.moveTo(-1.1, 0.62);
  cabinShape.lineTo(0.9, 0.62);
  cabinShape.lineTo(0.35, 1.45);
  cabinShape.lineTo(-0.7, 1.45);
  cabinShape.closePath();

  const cabinGeo = new THREE.ExtrudeGeometry(cabinShape, {
    depth: CABIN_WIDTH,
    bevelEnabled: false,
  });
  cabinGeo.rotateY(-Math.PI / 2);
  cabinGeo.translate(CABIN_WIDTH / 2, 0, 0);

  const cabin = new THREE.Mesh(cabinGeo, cabinMaterial);
  cabin.name = 'cabin';
  root.add(cabin);

  // Wheels.
  const wheels = new THREE.Group();
  wheels.name = 'wheels';

  const wheelGeo = new THREE.CylinderGeometry(
    WHEEL_RADIUS,
    WHEEL_RADIUS,
    WHEEL_WIDTH,
    10
  );
  wheelGeo.rotateZ(Math.PI / 2);

  const hubGeo = new THREE.CylinderGeometry(
    HUB_RADIUS,
    HUB_RADIUS,
    HUB_THICKNESS,
    8
  );
  hubGeo.rotateZ(Math.PI / 2);

  const wheelPositions = [
    { x: -WHEEL_X, z: -WHEEL_Z },
    { x:  WHEEL_X, z: -WHEEL_Z },
    { x: -WHEEL_X, z:  WHEEL_Z },
    { x:  WHEEL_X, z:  WHEEL_Z },
  ];

  for (const pos of wheelPositions) {
    const wheel = new THREE.Mesh(wheelGeo, wheelMaterial);
    wheel.position.set(pos.x, WHEEL_RADIUS, pos.z);
    wheels.add(wheel);

    const outerX = pos.x > 0
      ? pos.x + WHEEL_WIDTH / 2 + HUB_THICKNESS / 2
      : pos.x - WHEEL_WIDTH / 2 - HUB_THICKNESS / 2;

    const hub = new THREE.Mesh(hubGeo, hubMaterial);
    hub.position.set(outerX, WHEEL_RADIUS, pos.z);
    wheels.add(hub);
  }

  root.add(wheels);

  // Front lights.
  const lightsFront = new THREE.Group();
  lightsFront.name = 'lights-front';

  const headlightGeo = new THREE.BoxGeometry(0.24, 0.1, 0.06);

  const headlightL = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightL.position.set(-0.55, 0.42, dims.length / 2 - 0.03);

  const headlightR = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightR.position.set(0.55, 0.42, dims.length / 2 - 0.03);

  lightsFront.add(headlightL, headlightR);
  root.add(lightsFront);

  // Rear lights.
  const lightsRear = new THREE.Group();
  lightsRear.name = 'lights-rear';

  const tailLightGeo = new THREE.BoxGeometry(0.24, 0.1, 0.06);

  const tailLightL = new THREE.Mesh(tailLightGeo, tailLightMaterial);
  tailLightL.position.set(-0.55, 0.42, -dims.length / 2 + 0.03);

  const tailLightR = new THREE.Mesh(tailLightGeo, tailLightMaterial);
  tailLightR.position.set(0.55, 0.42, -dims.length / 2 + 0.03);

  lightsRear.add(tailLightL, tailLightR);
  root.add(lightsRear);

  return root;
}
